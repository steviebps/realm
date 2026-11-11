import { copyValue, normalizeContext, typedValue } from './evaluate.js';
import { isRecord, Snapshot } from './snapshot.js';
import { decodeFlags, isPromise, storageKey, touch, type StorageAdapter } from './storage.js';
import { asError, pollingInterval, RealmError, Transport, type ClientOptions, type Following } from './transport.js';
import type { EvaluatedFlags, EvaluationContext, FlagType } from './types.js';

/** Options for a BrowserClient. */
export interface BrowserClientOptions extends ClientOptions {
  /** Who flags are evaluated for; change it later with identify. */
  context?: EvaluationContext;
  /**
   * Evaluated values for `context` to serve until the server's evaluation
   * arrives, for example from a server render or shipped with the app. They
   * apply only to that context, never to one identify switches to, and are
   * never stored. Values in storage for the context win over them.
   */
  bootstrap?: EvaluatedFlags;
  /**
   * Where to keep the latest evaluated values for each context (never the
   * rules), so that a start without a network serves them instead of
   * fallbacks; see localStorageAdapter. Values are written whenever they
   * change, and read back only for the context they were evaluated for. For
   * each chamber, only the ten most recently used contexts' values are kept.
   */
  storage?: StorageAdapter;
}

/**
 * Where a client's current values came from: the bootstrap option, storage
 * (an earlier evaluation for the same context), or the server just now.
 */
export type FlagSource = 'bootstrap' | 'cache' | 'network';

const defaultPollingInterval = 5 * 60_000;
const noFlags: EvaluatedFlags = Object.freeze({}) as EvaluatedFlags;

interface Evaluation {
  flags: EvaluatedFlags;
}

/**
 * Asks the server to evaluate flags for one context, using an `evaluate` key.
 * The rules never reach the client, so it is safe in browsers and other
 * untrusted code.
 */
export class BrowserClient {
  readonly #transport: Transport;
  readonly #options: BrowserClientOptions;
  readonly #evaluation = new Snapshot<Evaluation>({ flags: {} }, asEvaluation);
  #context: EvaluationContext;
  // The context as JSON, and the key its values are stored under.
  #contextKey: string;
  #storageKey: string;
  // The prefix of every storage key: the evaluate URL (server and chamber).
  readonly #storagePrefix: string;
  // The bootstrap values, and the context (as JSON) they are for.
  readonly #bootstrap: { contextKey: string; flags: EvaluatedFlags } | undefined;
  // Where the snapshot's values came from; undefined while it is empty and
  // getters return fallbacks. They are always the current context's.
  #source: FlagSource | undefined;
  // The server's revision of the values, while they came from the server
  // and it reported one.
  #revision: number | undefined;
  // What storage is known to hold under #storageKey, so unchanged values are
  // not written again.
  #stored: string | undefined;
  // The storage key last marked as recently used.
  #touched: string | undefined;
  // The background refresh, while started.
  #running: { controller: AbortController; following: Following } | undefined;

  constructor(options: BrowserClientOptions) {
    this.#options = options;
    this.#transport = new Transport(options);
    this.#storagePrefix = this.#transport.url('evaluate');
    this.#context = options.context ?? {};
    this.#contextKey = JSON.stringify(this.#context);
    this.#storageKey = this.#keyFor(this.#context);
    this.#bootstrap = isRecord(options.bootstrap)
      ? { contextKey: this.#contextKey, flags: options.bootstrap }
      : undefined;
    this.#restore();
  }

  /**
   * Evaluates flags for the context, then keeps them current in the
   * background. It rejects when the first evaluation fails, but keeps
   * retrying with backoff until stop; isReady reports when values arrive.
   * Calling it again while started reflects the current state: it waits for
   * the first evaluation while that is in flight; after that, it resolves
   * once any evaluation has arrived, and otherwise rejects with the latest
   * failure.
   */
  async start(): Promise<void> {
    if (this.#running) return this.#running.following.current();
    return this.#connect();
  }

  /**
   * Switches to a new context and re-evaluates every flag for it. Until the
   * new evaluation arrives, isReady is false and getters return values stored
   * for the new context, if there are any, or else their fallbacks: values
   * evaluated for another context (another user on a shared device, say) are
   * never served for this one. The client keeps retrying if the evaluation
   * fails. On a stopped client it only sets the context.
   */
  async identify(context: EvaluationContext): Promise<void> {
    if (context === undefined) context = {};
    const contextKey = JSON.stringify(context);
    this.#context = context;
    if (contextKey !== this.#contextKey) {
      this.#contextKey = contextKey;
      this.#storageKey = this.#keyFor(context);
      this.#restore();
    }
    if (this.#running) await this.#connect();
  }

  /** Stops refreshing. The last known values stay available. */
  stop(): void {
    this.#running?.controller.abort();
    this.#running = undefined;
  }

  /**
   * Whether the values are the server's evaluation for the current context.
   * Bootstrap and stored values do not count, even for the same context.
   */
  isReady(): boolean {
    return this.#source === 'network';
  }

  /**
   * Where the current values came from, or undefined while there are none
   * and getters return their fallbacks.
   */
  source(): FlagSource | undefined {
    return this.#source;
  }

  /**
   * The server's storage revision of the chamber the current values were
   * evaluated from, for example to record which configuration a page ran
   * under. It is the highest revision among the chamber and the parents it
   * inherits from. It is undefined while the values are not the server's
   * (see source), and when the server does not report revisions (only
   * servers on transactional storage, such as postgres, etcd, or boltdb, do).
   * A write that leaves this context's values unchanged is not sent, so the
   * revision can be older than the chamber's while describing the same
   * values.
   */
  revision(): number | undefined {
    return this.#revision;
  }

  /**
   * The context flags are evaluated for, as JSON: compare it to tell whether
   * the values are for a given context.
   */
  contextKey(): string {
    return this.#contextKey;
  }

  /**
   * Calls listener whenever any flag's value changes, or isReady or source
   * does. Returns a function that unsubscribes.
   */
  subscribe(listener: () => void): () => void {
    return this.#evaluation.subscribe(listener);
  }

  /** The value of a boolean flag, or fallback. */
  bool(flag: string, fallback: boolean): boolean {
    return this.#typed(flag, 'boolean', fallback);
  }

  /** The value of a string flag, or fallback. */
  string(flag: string, fallback: string): string {
    return this.#typed(flag, 'string', fallback);
  }

  /** The value of a number flag, or fallback. */
  number(flag: string, fallback: number): number {
    return this.#typed(flag, 'number', fallback);
  }

  /**
   * The value of a custom (JSON) flag, or fallback. The value is a copy:
   * changing it does not change what later reads return.
   */
  json<T>(flag: string, fallback: T): T {
    return this.#typed(flag, 'custom', fallback, true);
  }

  /**
   * Every flag's current value. The object is shared and stays the same
   * until a value changes (React's hooks rely on that), so treat it as read
   * only; json returns a copy of one value.
   */
  allFlags(): EvaluatedFlags {
    const flags = this.#evaluation.value.flags;
    return isRecord(flags) ? flags : noFlags;
  }

  // typed returns the value of flag when it has type want (a copy, with
  // copy), and otherwise fallback. It never throws: a flag of a shape this
  // SDK does not expect returns fallback too.
  #typed<T>(key: string, want: FlagType, fallback: T, copy = false): T {
    try {
      const flags = this.allFlags();
      const flag = Object.hasOwn(flags, key) ? flags[key] : undefined;
      if (!flag) return fallback;
      const value = typedValue(flag.type, flag.value, want, fallback);
      return copy && value !== fallback ? copyValue(value) : value;
    } catch {
      return fallback;
    }
  }

  // keyFor returns the storage key of context's values, or '' without
  // storage. Contexts are keyed as the server evaluates them, normalized.
  #keyFor(context: EvaluationContext): string {
    return this.#options.storage ? storageKey(this.#storagePrefix, normalizeContext(context)) : '';
  }

  // show records that the snapshot holds values from source, then applies
  // update, telling listeners if only the source changed.
  #show(source: FlagSource | undefined, update: () => boolean): void {
    const before = this.#source;
    this.#source = source;
    let changed: boolean;
    try {
      changed = update();
    } catch (e) {
      this.#source = before;
      throw e;
    }
    if (!changed && before !== source) this.#evaluation.notify();
  }

  // restore replaces the values with the best ones available for the current
  // context before the server answers: stored values, else bootstrap values,
  // else none. Storage that answers asynchronously applies its values later,
  // if the context is unchanged and the server has not answered by then.
  #restore(): void {
    this.#revision = undefined;
    const contextKey = this.#contextKey;
    const storage = this.#options.storage;
    this.#stored = undefined;
    if (storage) {
      let stored: ReturnType<StorageAdapter['getItem']>;
      try {
        stored = storage.getItem(this.#storageKey);
      } catch (e) {
        this.#reportStorage(e);
      }
      if (isPromise(stored)) {
        stored
          // by now the server's values, or other stored ones, may be shown
          .then((raw) => this.#source !== 'network' && this.#source !== 'cache' && this.#applyStored(contextKey, raw))
          .catch((e: unknown) => this.#reportStorage(e));
      } else if (this.#applyStored(contextKey, stored)) {
        return;
      }
    }
    const bootstrap = this.#bootstrap;
    if (bootstrap?.contextKey === contextKey) {
      this.#show('bootstrap', () => this.#evaluation.set({ flags: bootstrap.flags }));
    } else {
      this.#show(undefined, () => this.#evaluation.set({ flags: {} }));
    }
  }

  // applyStored serves the values raw holds for contextKey, unless the
  // context has changed since they were read or raw holds no evaluation. It
  // reports whether it served them.
  #applyStored(contextKey: string, raw: string | null | undefined): boolean {
    if (contextKey !== this.#contextKey) return false;
    const flags = decodeFlags(raw);
    if (!flags) return false;
    this.#stored = raw!;
    this.#show('cache', () => this.#evaluation.setRaw(raw!, () => ({ flags })));
    return true;
  }

  // persist stores the current values for the current context, unless
  // storage already holds them, and marks the context as recently used,
  // which removes the values of the least recently used beyond the limit.
  #persist(): void {
    const storage = this.#options.storage;
    if (!storage) return;
    const key = this.#storageKey;
    const raw = this.#evaluation.raw;
    // Mark first: removing old values makes room for these.
    if (key !== this.#touched) {
      this.#touched = key;
      this.#store(() => touch(storage, this.#storagePrefix, key));
    }
    if (raw !== this.#stored) {
      this.#stored = raw;
      this.#store(() => storage.setItem(key, raw));
    }
  }

  // store calls write, reporting a failure, thrown or rejected, to onError.
  #store(write: () => void | Promise<void>): void {
    try {
      const done = write();
      if (isPromise(done)) done.catch((e: unknown) => this.#reportStorage(e));
    } catch (e) {
      this.#reportStorage(e);
    }
  }

  #reportStorage(e: unknown): void {
    this.#transport.report(new Error(`flag storage failed: ${asError(e).message}`));
  }

  // connect evaluates for the current context, replacing any refresh for an
  // earlier one, whose results are then dropped.
  #connect(): Promise<void> {
    this.#running?.controller.abort();
    const controller = new AbortController();
    // receive serves the server's values for the current context (a
    // superseded evaluation is aborted before it can deliver), applied by
    // update, and stores them.
    const receive = (update: () => boolean, revision: number | undefined) => {
      this.#show('network', update);
      this.#revision = revision;
      this.#persist();
    };
    const following = this.#transport.follow(
      'evaluate',
      // The server accepts only strings, as the key and attribute values.
      { method: 'POST', body: JSON.stringify(normalizeContext(this.#context)) },
      'flags',
      {
        set: (data, revision) => receive(() => this.#evaluation.set(data), revision),
        setRaw: (raw, revision) => receive(() => this.#evaluation.setRaw(raw), revision),
      },
      this.#options.streaming ?? true,
      pollingInterval(this.#options.pollingInterval, defaultPollingInterval),
      controller.signal,
    );
    this.#running = { controller, following };
    return following.started.catch((e: unknown) => {
      // identify or stop aborted this evaluation and may have started another.
      if (controller.signal.aborted) return;
      // Stored or bootstrap values for the context stay in use.
      throw e;
    });
  }
}

// asEvaluation returns data as an evaluation, or throws when it is not one: a
// gateway or maintenance page in front of the server may answer with other
// JSON.
function asEvaluation(data: unknown): Evaluation {
  if (!isRecord(data) || !isRecord(data.flags)) throw new RealmError('the server did not return an evaluation');
  return data as unknown as Evaluation;
}
