import {
  attributeValue,
  copyValue,
  evaluateChamber,
  flagValue,
  stringAttributes,
  typedValue,
  VERSION_ATTRIBUTE,
} from './evaluate.js';
import { isRecord, Snapshot } from './snapshot.js';
import { pollingInterval, RealmError, Transport, type ClientOptions, type Following } from './transport.js';
import type { Chamber, EvaluatedFlags, EvaluationContext, FlagType } from './types.js';

/** Options for a ServerClient. */
export interface ServerClientOptions extends ClientOptions {
  /** Process-wide attributes, such as `environment`, used when a context does not set them. */
  attributes?: Record<string, string>;
  /** This application's version, exposed as the `version` attribute. */
  version?: string;
}

const defaultPollingInterval = 15 * 60_000;

/**
 * Reads a chamber with a `read` key and evaluates flags locally, like the Go
 * SDK. Use it in backend services; browsers should use BrowserClient, which
 * never sees the rules.
 */
export class ServerClient {
  readonly #transport: Transport;
  readonly #options: ServerClientOptions;
  readonly #defaults: Record<string, string>;
  readonly #chamber = new Snapshot<Chamber>({ rules: {} }, asChamber);
  // The background refresh, while started.
  #running: { controller: AbortController; following: Following } | undefined;

  constructor(options: ServerClientOptions) {
    this.#options = options;
    this.#transport = new Transport(options);
    // as strings, as contexts are read
    this.#defaults = { ...stringAttributes(options.attributes) };
    const version = attributeValue(options.version);
    if (version) this.#defaults[VERSION_ATTRIBUTE] = version;
  }

  /**
   * Loads the chamber, then keeps it current in the background. It rejects
   * when the first load fails (for example, a bad key or a missing chamber),
   * but keeps retrying with backoff until stop. Calling it again while
   * started reflects the current state: it waits for the first load while
   * that is in flight; after that, it resolves once any load has succeeded,
   * and otherwise rejects with the latest failure.
   */
  async start(): Promise<void> {
    if (this.#running) return this.#running.following.current();
    const controller = new AbortController();
    const following = this.#transport.follow(
      'chambers',
      { method: 'GET' },
      'chamber',
      this.#chamber,
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
   * Whether the client has a chamber from the server; until it does, getters
   * return their fallbacks. It stays true after stop, as the last known
   * values stay in use.
   */
  isReady(): boolean {
    return this.#chamber.raw !== '';
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
   * A chamber that changes during a request changes the revision too; read
   * it alongside the flags it describes.
   */
  revision(): number | undefined {
    return this.#chamber.revision;
  }

  /** Calls listener whenever the chamber changes. Returns a function that unsubscribes. */
  subscribe(listener: () => void): () => void {
    return this.#chamber.subscribe(listener);
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
      const flags = evaluateChamber(this.#chamber.value, context ?? {}, this.#defaults);
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
      const rules = this.#chamber.value.rules;
      const flag = Object.hasOwn(rules, key) ? rules[key] : undefined;
      if (!flag) return fallback;
      const value = typedValue(flag.type, flagValue(key, flag, context ?? {}, this.#defaults), want, fallback);
      return copy && value !== fallback ? copyValue(value) : value;
    } catch {
      return fallback;
    }
  }
}

// asChamber returns data as a chamber, or throws when it is not one: a
// gateway or maintenance page in front of the server may answer with other
// JSON.
function asChamber(data: unknown): Chamber {
  if (!isRecord(data) || !isRecord(data.rules)) throw new RealmError('the server did not return a chamber');
  return data as unknown as Chamber;
}
