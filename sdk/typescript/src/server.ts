import {
  attributeValue,
  copyValue,
  evaluateChamber,
  flagValue,
  normalizeContext,
  stringAttributes,
  typedValue,
  VERSION_ATTRIBUTE,
} from './evaluate.js';
import { ChamberSource, SubtreeSource, type Source } from './source.js';
import { parseTemplate } from './template.js';
import { pollingInterval, Transport, type ClientOptions, type Following } from './transport.js';
import type { Chamber, EvaluatedFlags, EvaluationContext, FlagType, InitialState } from './types.js';

/** Options for a ServerClient. */
export interface ServerClientOptions extends ClientOptions {
  /** Process-wide attributes, such as `environment`, used when a context does not set them. */
  attributes?: Record<string, string>;
  /** This application's version, exposed as the `version` attribute. */
  version?: string;
  /**
   * Instead of `path`: a template, such as
   * `/prod/tenants/{tenantId}/{locationId}/`, that picks the chamber for each
   * context. The client reads (and watches) every chamber below the
   * template's root, its literal prefix (here `/prod/tenants/`), and each
   * getter fills the placeholders with the context's attributes, read as
   * targeting clauses read them (so `{key}` is the context's key, and
   * process-wide attributes fill in for missing ones). It evaluates against
   * the deepest of those paths that has a chamber: a location with no
   * chamber gets its tenant's, and a tenant with none gets the root's.
   * Resolution stops at a value that is missing or empty, contains `/` or
   * `..`, or is `.`. See chamberPath.
   *
   * A placeholder is a whole segment, `{name}`, where name is one or more of
   * `A-Z a-z 0-9 _ - .`; other segments are literal text without braces. The
   * constructor throws a TypeError for an invalid template, or one given
   * with `path`.
   */
  pathTemplate?: string;
}

const defaultPollingInterval = 15 * 60_000;

/**
 * Reads a chamber with a `read` key and evaluates flags locally, like the Go
 * SDK. Use it in backend services; browsers should use BrowserClient, which
 * never sees the rules. With the pathTemplate option, it reads every chamber
 * below a path and picks one for each context.
 */
export class ServerClient {
  readonly #transport: Transport;
  readonly #options: ServerClientOptions;
  readonly #defaults: Record<string, string>;
  // What the client follows: one chamber at a path, or with a template, the
  // subtree below its root. Nothing else depends on which (see Source).
  readonly #source: Source;
  // The background refresh, while started.
  #running: { controller: AbortController; following: Following } | undefined;

  constructor(options: ServerClientOptions) {
    this.#options = options;
    if (options.pathTemplate !== undefined) {
      if (options.path !== undefined) throw new TypeError('the path and pathTemplate options cannot be combined');
      this.#source = new SubtreeSource(parseTemplate(options.pathTemplate));
    } else {
      this.#source = new ChamberSource(options.path);
    }
    this.#transport = new Transport({ ...options, path: this.#source.readPath });
    // as strings, as contexts are read
    this.#defaults = { ...stringAttributes(options.attributes) };
    const version = attributeValue(options.version);
    if (version) this.#defaults[VERSION_ATTRIBUTE] = version;
  }

  /**
   * Loads the chamber (with pathTemplate, the subtree), then keeps it
   * current in the background. It rejects when the first load fails (for
   * example, a bad key or a missing chamber), but keeps retrying with
   * backoff until stop. Calling it again while started reflects the current
   * state: it waits for the first load while that is in flight; after that,
   * it resolves once any load has succeeded, and otherwise rejects with the
   * latest failure.
   */
  async start(): Promise<void> {
    if (this.#running) return this.#running.following.current();
    const controller = new AbortController();
    const following = this.#transport.follow(
      'chambers',
      { method: 'GET' },
      this.#source.feed,
      this.#options.streaming ?? true,
      pollingInterval(this.#options.pollingInterval, defaultPollingInterval),
      controller.signal,
    );
    this.#running = { controller, following };
    return following.started.catch((e: unknown) => {
      // A stop aborted this load; the client may have been started again since.
      if (!controller.signal.aborted) throw e;
    });
  }

  /** Stops refreshing. The last known values stay available. */
  stop(): void {
    this.#running?.controller.abort();
    this.#running = undefined;
  }

  /**
   * Whether the client has a chamber (with pathTemplate, a subtree) from
   * the server; until it does, getters return their fallbacks. It stays
   * true after stop, as the last known values stay in use.
   */
  isReady(): boolean {
    return this.#source.loaded();
  }

  /**
   * The server's storage revision of the chamber the client evaluates, for
   * example to record on spans or logs which configuration a request ran
   * under. It is the highest revision among the chamber and the parents it
   * inherits from, so it changes whenever any of them does. It is undefined
   * until a chamber arrives, and when the server does not report revisions
   * (only servers on transactional storage, such as postgres, etcd, or
   * boltdb, do). Revisions name a version of the chamber rather than order
   * versions: deleting a parent chamber can lower it.
   *
   * With pathTemplate, it is the revision of the chamber context resolves
   * to (see chamberPath), and undefined when it resolves to none; without,
   * context is ignored.
   *
   * A chamber that changes during a request changes the revision too; read
   * it alongside the flags it describes.
   */
  revision(context?: EvaluationContext): number | undefined {
    return this.#target(context)?.revision;
  }

  /**
   * The path of the chamber the client evaluates for context, such as
   * `/prod/tenants/t-42/`, or undefined when it has none (before the first
   * load, say). With pathTemplate, it is the deepest path the template
   * resolves to for context that has a chamber, so it shows which chamber a
   * context with a mistyped or missing attribute fell back to; without, it
   * is the path option, once the chamber has arrived.
   */
  chamberPath(context?: EvaluationContext): string | undefined {
    return this.#target(context)?.path;
  }

  /**
   * Calls listener whenever the chamber (with pathTemplate, any chamber of
   * the subtree) changes. Returns a function that unsubscribes.
   */
  subscribe(listener: () => void): () => void {
    return this.#source.subscribe(listener);
  }

  /** The value of a boolean flag for context, or fallback. */
  bool(flag: string, context: EvaluationContext | undefined, fallback: boolean): boolean {
    return this.#typed(flag, context, 'boolean', fallback);
  }

  /** The value of a string flag for context, or fallback. */
  string(flag: string, context: EvaluationContext | undefined, fallback: string): string {
    return this.#typed(flag, context, 'string', fallback);
  }

  /** The value of a number flag for context, or fallback. */
  number(flag: string, context: EvaluationContext | undefined, fallback: number): number {
    return this.#typed(flag, context, 'number', fallback);
  }

  /**
   * The value of a custom (JSON) flag for context, or fallback. The value is
   * a copy: changing it does not change what later reads return.
   */
  json<T>(flag: string, context: EvaluationContext | undefined, fallback: T): T {
    return this.#typed(flag, context, 'custom', fallback, true);
  }

  /**
   * Every flag's value for context, for example to bootstrap a page. Custom
   * values are copies, as json returns them.
   */
  allFlags(context?: EvaluationContext): EvaluatedFlags {
    try {
      return this.#evaluate(this.#target(context)?.chamber, context ?? {}, this.#defaults);
    } catch {
      return {};
    }
  }

  /**
   * Every flag's value for context, for a page rendered on the server: pass
   * it to the BrowserClient (its initialState option) that renders the page,
   * and send it with the page to the one that hydrates it, so the first
   * render already has the right values in both places.
   *
   * It evaluates context as the server's evaluation endpoint does, which
   * the BrowserClient then asks for, so the values do not change when its
   * evaluation arrives: the process-wide attributes and version are not
   * applied (set them in the context instead), and number and boolean
   * attributes are sent as strings. The state carries the path of the
   * chamber evaluated (with pathTemplate, resolved the same way), which a
   * BrowserClient without a path option evaluates too.
   *
   * Until the client has a chamber for context (before its first load, or
   * with pathTemplate, when context resolves to none), the state has no
   * flags: a page renders fallbacks, not ready, rather than an empty
   * evaluation presented as the server's.
   */
  initialState(context?: EvaluationContext): InitialState {
    const normalized = normalizeContext(context);
    const state: InitialState = { context: normalized };
    // as the evaluation endpoint does: no process-wide attributes
    const target = this.#source.pick(normalized, {});
    if (!target) return state;
    try {
      state.flags = this.#evaluate(target.chamber, normalized, {});
    } catch {
      return state; // a chamber of a shape this SDK does not expect
    }
    state.path = target.path;
    if (target.revision !== undefined) state.revision = target.revision;
    return state;
  }

  // evaluate returns every flag's value in chamber for context, with defaults
  // filling in for the attributes it does not set. Custom values are copies.
  // It throws for a chamber of a shape this SDK does not expect.
  #evaluate(chamber: Chamber | undefined, context: EvaluationContext, defaults: Record<string, string>): EvaluatedFlags {
    if (!chamber) return {};
    const flags = evaluateChamber(chamber, context, defaults);
    for (const flag of Object.values(flags)) flag.value = copyValue(flag.value);
    return flags;
  }

  // typed returns the value of flag for context when it has type want (a
  // copy, with copy), and otherwise fallback. It never throws: a flag of a
  // shape this SDK does not expect returns fallback too.
  #typed<T>(key: string, context: EvaluationContext | undefined, want: FlagType, fallback: T, copy = false): T {
    try {
      const rules = this.#target(context)?.chamber.rules;
      const flag = rules && Object.hasOwn(rules, key) ? rules[key] : undefined;
      if (!flag) return fallback;
      const value = typedValue(flag.type, flagValue(key, flag, context ?? {}, this.#defaults), want, fallback);
      return copy && value !== fallback ? copyValue(value) : value;
    } catch {
      return fallback;
    }
  }

  // target returns the chamber context is evaluated against, with the
  // process-wide attributes filling in for those it does not set.
  #target(context: EvaluationContext | undefined) {
    return this.#source.pick(context ?? {}, this.#defaults);
  }
}

// Characters escaped by serializeInitialState: those that could end or
// comment out a script element, and the line separators older JavaScript
// parsers reject in string literals.
const unsafeInScript = /[<>&\u2028\u2029]/g;

/**
 * The JSON text of state, safe to place inside an HTML `<script>` element:
 * `<`, `>`, `&`, U+2028, and U+2029 are written as `\u` escapes, so a
 * context attribute such as `</script>` cannot end the element. It parses
 * back with JSON.parse, or as a JavaScript expression. For example:
 *
 * ```html
 * <script id="realm-state" type="application/json">${serializeInitialState(state)}</script>
 * ```
 */
export function serializeInitialState(state: InitialState): string {
  return JSON.stringify(state).replace(unsafeInScript, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`);
}
