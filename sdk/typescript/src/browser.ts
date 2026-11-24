import { copyValue, normalizeContext, typedValue } from './evaluate.js';
import { isRecord, Snapshot } from './snapshot.js';
import { canonicalContext, decodeFlags, isPromise, storageKey, touch, type StorageAdapter } from './storage.js';
import { asError, pollingInterval, RealmError, sinkFeed, Transport, type ClientOptions, type Following } from './transport.js';
import type { EvaluatedFlags, EvaluationContext, FlagType, InitialState } from './types.js';

/** Options for a BrowserClient. */
export interface BrowserClientOptions extends ClientOptions {
  /**
   * Who flags are evaluated for; change it later with identify. It defaults
   * to the context of initialState, if set.
   */
  context?: EvaluationContext;
  /**
   * Flags a ServerClient evaluated for a page rendered on the server (see
   * ServerClient.initialState): give the same state to the client that
   * renders the page there and to the one that hydrates it in the browser.
   * Its values are served, with source `render`, whenever the context is
   * the one they were evaluated for (compared as the server receives
   * contexts), and count as ready: they are the server's rules evaluated
   * for that context, so they win over stored and bootstrap values. Once
   * the server's own evaluation for that context arrives, they are only
   * served as bootstrap values. They are never stored.
   */
  initialState?: InitialState;
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
 * Where a client's current values came from: the initialState option (a
 * server render's evaluation), the bootstrap option, storage (an earlier
 * evaluation for the same context), or the server just now.
 */
export type FlagSource = 'render' | 'bootstrap' | 'cache' | 'network';

const defaultPollingInterval = 5 * 60_000;
const noFlags: EvaluatedFlags = Object.freeze({}) as EvaluatedFlags;

interface Evaluation {
  flags: EvaluatedFlags;
}

// Values given to a client before it starts, and the context they are for:
// as JSON (contextKey, for bootstrap) or as the server receives contexts
// (match, for initialState).
interface Given {
  contextKey: string;
  match: string;
  flags: EvaluatedFlags;
  revision: number | undefined;
}

// What a client was given at construction, which never changes: a server
// render and the hydration of its page read the same values from it.
interface Construction {
  contextKey: string;
  match: string;
  initial: Given | undefined;
  bootstrap: Given | undefined;
}

const constructions = new WeakMap<BrowserClient, Construction>();

/**
 * The values a server render reads from client for context (the context it
 * was constructed with, if undefined), and their source: the initialState
 * values when they are for that context, else the bootstrap values when they
 * are, else undefined. It depends only on the client's options, never on
 * what it has received since, so a page renders the same on the server and
 * while it hydrates in the browser, however late that is.
 */
export function renderedValues(
  client: BrowserClient,
  context: EvaluationContext | undefined,
): { flags: EvaluatedFlags; source: FlagSource } | undefined {
  const given = constructions.get(client);
  if (!given) return undefined;
  const contextKey = context === undefined ? given.contextKey : JSON.stringify(context);
  const match = context === undefined ? given.match : matchKey(context);
  if (given.initial?.match === match) return { flags: given.initial.flags, source: 'render' };
  if (given.bootstrap?.contextKey === contextKey) return { flags: given.bootstrap.flags, source: 'bootstrap' };
  return undefined;
}

/**
 * The value of flag in flags when it has type want (a copy, with copy), and
 * otherwise fallback. It never throws: a flag of a shape this SDK does not
 * expect returns fallback too.
 */
export function readFlag<T>(flags: EvaluatedFlags, key: string, want: FlagType, fallback: T, copy = false): T {
  try {
    const flag = Object.hasOwn(flags, key) ? flags[key] : undefined;
    if (!flag) return fallback;
    const value = typedValue(flag.type, flag.value, want, fallback);
    return copy && value !== fallback ? copyValue(value) : value;
  } catch {
    return fallback;
  }
}

// matchKey returns context as the server receives it, in a canonical form:
// contexts it returns the same key for are evaluated alike.
function matchKey(context: EvaluationContext | undefined): string {
  return canonicalContext(normalizeContext(context));
}

// given returns the values of a bootstrap or initialState option, if it has
// any, for context.
function given(context: EvaluationContext, flags: unknown, revision?: unknown): Given | undefined {
  if (!isRecord(flags)) return undefined;
  return {
    contextKey: JSON.stringify(context),
    match: matchKey(context),
    flags: flags as EvaluatedFlags,
    revision: typeof revision === 'number' && Number.isSafeInteger(revision) ? revision : undefined,
  };
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
  // The context as the server receives it, to compare with initialState's.
  #match: string;
  // The initialState values, and whether they are still the newest known
  // for their context: no evaluation has arrived for it since.
  readonly #initial: (Given & { fresh: boolean }) | undefined;
  // The bootstrap values, and the context (as JSON) they are for.
  readonly #bootstrap: Given | undefined;
  // Where the snapshot's values came from; undefined while it is empty and
  // getters return fallbacks. They are always the current context's.
  #source: FlagSource | undefined;
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
    const initialState = isRecord(options.initialState) ? options.initialState : undefined;
    const initialContext = isRecord(initialState?.context) ? (initialState.context as EvaluationContext) : {};
    this.#context = options.context ?? initialContext;
    this.#contextKey = JSON.stringify(this.#context);
    this.#match = matchKey(this.#context);
    this.#storageKey = this.#keyFor(this.#context);
    const initial = given(initialContext, initialState?.flags, initialState?.revision);
    this.#initial = initial && { ...initial, fresh: true };
    this.#bootstrap = given(this.#context, options.bootstrap);
    constructions.set(this, { contextKey: this.#contextKey, match: this.#match, initial, bootstrap: this.#bootstrap });
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
      this.#match = matchKey(context);
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
   * Whether the values are the server's evaluation for the current context:
   * its own, or a server render's (the initialState option, until the
   * server's own evaluation arrives). Bootstrap and stored values do not
   * count, even for the same context.
   */
  isReady(): boolean {
    return this.#source === 'network' || this.#source === 'render';
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
   * inherits from. It is undefined while the values are not the server's or
   * a server render's (see source), and when the server does not report revisions (only
   * servers on transactional storage, such as postgres, etcd, or boltdb, do).
   * A write that leaves this context's values unchanged is not sent, so the
   * revision can be older than the chamber's while describing the same
   * values.
   */
  revision(): number | undefined {
    // values not from the server are set without one
    return this.#evaluation.revision;
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
  // copy), and otherwise fallback.
  #typed<T>(key: string, want: FlagType, fallback: T, copy = false): T {
    return readFlag(this.allFlags(), key, want, fallback, copy);
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
  // context before the server answers: a server render's values not yet
  // superseded, else stored values, else the server render's or bootstrap
  // values, else none. Storage that answers asynchronously applies its
  // values later, if the context is unchanged and neither the server nor a
  // server render has answered by then.
  #restore(): void {
    const contextKey = this.#contextKey;
    const storage = this.#options.storage;
    const initial = this.#initial?.match === this.#match ? this.#initial : undefined;
    this.#stored = undefined;
    if (initial?.fresh) {
      this.#show('render', () => this.#evaluation.set({ flags: initial.flags }, initial.revision));
      return;
    }
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
          .then((raw) => (this.#source === 'bootstrap' || this.#source === undefined) && this.#applyStored(contextKey, raw))
          .catch((e: unknown) => this.#reportStorage(e));
      } else if (this.#applyStored(contextKey, stored)) {
        return;
      }
    }
    const bootstrap = initial ?? (this.#bootstrap?.contextKey === contextKey ? this.#bootstrap : undefined);
    if (bootstrap) {
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
    this.#show('cache', () => this.#evaluation.setRaw(raw!, undefined, () => ({ flags })));
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
    const receive = (update: () => boolean) => {
      this.#show('network', update);
      // A server render's values for this context are older now.
      if (this.#initial?.match === this.#match) this.#initial.fresh = false;
      this.#persist();
    };
    const following = this.#transport.follow(
      'evaluate',
      // The server accepts only strings, as the key and attribute values.
      { method: 'POST', body: JSON.stringify(normalizeContext(this.#context)) },
      sinkFeed('flags', {
        set: (data, revision) => receive(() => this.#evaluation.set(data, revision)),
        setRaw: (raw, revision) => receive(() => this.#evaluation.setRaw(raw, revision)),
      }),
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
