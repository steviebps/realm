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
import { isRecord, Snapshot } from './snapshot.js';
import { Subtree, type View } from './subtree.js';
import { parseTemplate, resolvePath, type PathTemplate } from './template.js';
import {
  pollingInterval,
  RealmError,
  sinkFeed,
  Transport,
  type ClientOptions,
  type Feed,
  type Following,
} from './transport.js';
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
  // subtree below its root.
  readonly #source:
    | { kind: 'path'; path: string; chamber: Snapshot<Chamber> }
    | { kind: 'template'; template: PathTemplate; subtree: Subtree };
  // The background refresh, while started.
  #running: { controller: AbortController; following: Following } | undefined;

  constructor(options: ServerClientOptions) {
    this.#options = options;
    if (options.pathTemplate !== undefined) {
      if (options.path !== undefined) throw new TypeError('the path and pathTemplate options cannot be combined');
      const template = parseTemplate(options.pathTemplate);
      this.#source = { kind: 'template', template, subtree: new Subtree() };
      this.#transport = new Transport({ ...options, path: template.root });
    } else {
      const segments = (options.path ?? '/').split('/').filter((s) => s !== '');
      const path = segments.length === 0 ? '/' : `/${segments.join('/')}/`;
      this.#source = { kind: 'path', path, chamber: new Snapshot<Chamber>({ rules: {} }, asChamber) };
      this.#transport = new Transport(options);
    }
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
    const source = this.#source;
    const feed: Feed = source.kind === 'path' ? sinkFeed('chamber', source.chamber) : source.subtree;
    const following = this.#transport.follow(
      'chambers',
      { method: 'GET' },
      feed,
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
    const source = this.#source;
    return source.kind === 'path' ? source.chamber.raw !== '' : source.subtree.loaded;
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
    const source = this.#source;
    return source.kind === 'path' ? source.chamber.revision : this.#view(context)?.revision;
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
    const source = this.#source;
    if (source.kind === 'path') return this.isReady() ? source.path : undefined;
    return this.#resolve(context);
  }

  /**
   * Calls listener whenever the chamber (with pathTemplate, any chamber of
   * the subtree) changes. Returns a function that unsubscribes.
   */
  subscribe(listener: () => void): () => void {
    const source = this.#source;
    return source.kind === 'path' ? source.chamber.subscribe(listener) : source.subtree.subscribe(listener);
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
    return this.#evaluate(context ?? {}, this.#defaults);
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
   * attributes are sent as strings. Give the BrowserClient the path of the
   * chamber this client evaluates (see chamberPath).
   */
  initialState(context?: EvaluationContext): InitialState {
    const normalized = normalizeContext(context);
    const state: InitialState = { context: normalized, flags: this.#evaluate(normalized, {}) };
    const source = this.#source;
    const revision = source.kind === 'path' ? source.chamber.revision : this.#view(normalized, {})?.revision;
    if (revision !== undefined) state.revision = revision;
    return state;
  }

  // evaluate returns every flag's value for context, with defaults filling in
  // for the attributes it does not set. Custom values are copies.
  #evaluate(context: EvaluationContext, defaults: Record<string, string>): EvaluatedFlags {
    try {
      const chamber = this.#chamber(context, defaults);
      if (!chamber) return {};
      const flags = evaluateChamber(chamber, context, defaults);
      for (const flag of Object.values(flags)) flag.value = copyValue(flag.value);
      return flags;
    } catch {
      return {};
    }
  }

  // typed returns the value of flag for context when it has type want (a
  // copy, with copy), and otherwise fallback. It never throws: a flag of a
  // shape this SDK does not expect returns fallback too.
  #typed<T>(key: string, context: EvaluationContext | undefined, want: FlagType, fallback: T, copy = false): T {
    try {
      const rules = this.#chamber(context)?.rules;
      const flag = rules && Object.hasOwn(rules, key) ? rules[key] : undefined;
      if (!flag) return fallback;
      const value = typedValue(flag.type, flagValue(key, flag, context ?? {}, this.#defaults), want, fallback);
      return copy && value !== fallback ? copyValue(value) : value;
    } catch {
      return fallback;
    }
  }

  // chamber returns the chamber to evaluate context against, or undefined
  // when there is none.
  #chamber(context: EvaluationContext | undefined, defaults = this.#defaults): Chamber | undefined {
    const source = this.#source;
    return source.kind === 'path' ? source.chamber.value : this.#view(context, defaults)?.chamber;
  }

  // view returns the view of the subtree that context resolves to, if any.
  #view(context: EvaluationContext | undefined, defaults = this.#defaults): View | undefined {
    if (this.#source.kind !== 'template') return undefined;
    const path = this.#resolve(context, defaults);
    return path === undefined ? undefined : this.#source.subtree.get(path);
  }

  // resolve returns the path of the subtree's view that context resolves
  // to, if any, with defaults filling in for the attributes it does not set.
  #resolve(context: EvaluationContext | undefined, defaults = this.#defaults): string | undefined {
    if (this.#source.kind !== 'template') return undefined;
    const subtree = this.#source.subtree;
    return resolvePath(this.#source.template, (path) => subtree.get(path) !== undefined, context ?? {}, defaults);
  }
}

// asChamber returns data as a chamber, or throws when it is not one: a
// gateway or maintenance page in front of the server may answer with other
// JSON.
function asChamber(data: unknown): Chamber {
  if (!isRecord(data) || !isRecord(data.rules)) throw new RealmError('the server did not return a chamber');
  return data as unknown as Chamber;
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
