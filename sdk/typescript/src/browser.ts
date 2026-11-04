import { typedValue } from './evaluate.js';
import { Snapshot } from './snapshot.js';
import { decodeFlags, encodeFlags, storageKey, type StorageAdapter } from './storage.js';
import { Transport, type ClientOptions } from './transport.js';
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
   * change, and read back only for the context they were evaluated for.
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

// Shown describes the values in the snapshot: the context (as JSON) they
// were evaluated for and where they came from.
interface Shown {
  contextKey: string;
  source: FlagSource;
}

/**
 * Asks the server to evaluate flags for one context, using an `evaluate` key.
 * The rules never reach the client, so it is safe in browsers and other
 * untrusted code.
 */
export class BrowserClient {
  readonly #transport: Transport;
  readonly #options: BrowserClientOptions;
  readonly #evaluation = new Snapshot<Evaluation>({ flags: {} });
  #context: EvaluationContext;
  // The context as JSON, and the context bootstrap values are for.
  #contextKey: string;
  readonly #bootstrapFor: string;
  // What the snapshot holds; undefined while it is empty and getters return
  // fallbacks. Its context is always the current one.
  #shown: Shown | undefined;
  // The background refresh, and its first evaluation, while started.
  #running: { controller: AbortController; started: Promise<void> } | undefined;

  constructor(options: BrowserClientOptions) {
    this.#options = options;
    this.#transport = new Transport(options);
    this.#context = options.context ?? {};
    this.#contextKey = JSON.stringify(this.#context);
    this.#bootstrapFor = this.#contextKey;
    this.#restore();
  }

  /**
   * Evaluates flags for the context, then keeps them current in the
   * background. It rejects when the first evaluation fails, but keeps
   * retrying with backoff until stop; isReady reports when values arrive.
   * Calling it again while started returns the first call's result.
   */
  async start(): Promise<void> {
    if (this.#running) return this.#running.started;
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
    this.#context = context;
    this.#contextKey = JSON.stringify(context);
    if (this.#shown?.contextKey !== this.#contextKey) this.#restore();
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
    return this.#shown?.source === 'network';
  }

  /**
   * Where the current values came from, or undefined while there are none
   * and getters return their fallbacks.
   */
  source(): FlagSource | undefined {
    return this.#shown?.source;
  }

  /** The context flags are evaluated for. */
  context(): EvaluationContext {
    return this.#context;
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

  /** The value of a custom (JSON) flag, or fallback. */
  json<T>(flag: string, fallback: T): T {
    return this.#typed(flag, 'custom', fallback);
  }

  /** Every flag's current value. */
  allFlags(): EvaluatedFlags {
    return this.#shown ? (this.#evaluation.value.flags ?? noFlags) : noFlags;
  }

  #typed<T>(key: string, want: FlagType, fallback: T): T {
    const flag = this.allFlags()[key];
    return flag ? typedValue(flag.type, flag.value, want, fallback) : fallback;
  }

  // show records that the snapshot holds shown's values, then applies update,
  // telling listeners if only the source changed. It reports whether a value
  // changed.
  #show(shown: Shown, update: () => boolean): boolean {
    const before = this.#shown;
    this.#shown = shown;
    let changed: boolean;
    try {
      changed = update();
    } catch (e) {
      this.#shown = before;
      throw e;
    }
    if (!changed && before?.source !== shown.source) this.#evaluation.notify();
    return changed;
  }

  // clear empties the snapshot, so getters return their fallbacks.
  #clear(): void {
    const before = this.#shown;
    this.#shown = undefined;
    if (!this.#evaluation.set({ flags: {} }) && before !== undefined) this.#evaluation.notify();
  }

  // restore replaces the values with the best ones available for the current
  // context before the server answers: stored values, else bootstrap values,
  // else none. Storage that answers asynchronously applies its values later,
  // if the context is unchanged and the server has not answered by then.
  #restore(): void {
    const contextKey = this.#contextKey;
    const storage = this.#options.storage;
    if (storage) {
      let stored: ReturnType<StorageAdapter['getItem']>;
      try {
        stored = storage.getItem(this.#storageKey(this.#context));
      } catch (e) {
        this.#reportStorage(e);
      }
      if (isPromise(stored)) {
        stored.then(
          (raw) => this.#applyStored(contextKey, raw),
          (e: unknown) => this.#reportStorage(e),
        );
      } else if (this.#applyStored(contextKey, stored)) {
        return;
      }
    }
    const bootstrap = this.#options.bootstrap;
    if (bootstrap && contextKey === this.#bootstrapFor) {
      this.#show({ contextKey, source: 'bootstrap' }, () => this.#evaluation.set({ flags: bootstrap }));
    } else {
      this.#clear();
    }
  }

  // applyStored serves the values raw holds for contextKey, unless the
  // context has changed since they were read, or values at least as fresh
  // (stored or from the server) are already shown. It reports whether it
  // served them.
  #applyStored(contextKey: string, raw: string | null | undefined): boolean {
    if (contextKey !== this.#contextKey) return false;
    const shown = this.#shown;
    if (shown?.contextKey === contextKey && (shown.source === 'network' || shown.source === 'cache')) return false;
    const flags = decodeFlags(raw);
    if (!flags) return false;
    this.#show({ contextKey, source: 'cache' }, () => this.#evaluation.set({ flags }));
    return true;
  }

  // persist stores the current values under key.
  #persist(key: string): void {
    const storage = this.#options.storage;
    if (!storage) return;
    try {
      const done = storage.setItem(key, encodeFlags(this.allFlags()));
      if (isPromise(done)) done.catch((e: unknown) => this.#reportStorage(e));
    } catch (e) {
      this.#reportStorage(e);
    }
  }

  #storageKey(context: EvaluationContext): string {
    return storageKey(this.#transport.url('evaluate'), context);
  }

  #reportStorage(e: unknown): void {
    const message = e instanceof Error ? e.message : String(e);
    this.#transport.onError(new Error(`flag storage failed: ${message}`));
  }

  // connect evaluates for the current context, replacing any refresh for an
  // earlier one, whose results are then dropped.
  #connect(): Promise<void> {
    this.#running?.controller.abort();
    const controller = new AbortController();
    const context = this.#context;
    const contextKey = this.#contextKey;
    const key = this.#options.storage ? this.#storageKey(context) : '';
    // receive serves the server's values for context, applied by update, and
    // stores them when they are new to storage.
    const receive = (update: () => boolean) => {
      const wasNetwork = this.#shown?.source === 'network';
      const changed = this.#show({ contextKey, source: 'network' }, update);
      if (changed || !wasNetwork) this.#persist(key);
    };
    const started = this.#transport
      .follow<Evaluation>(
        'evaluate',
        { method: 'POST', body: JSON.stringify(context) },
        'flags',
        {
          set: (value) => receive(() => this.#evaluation.set({ flags: value.flags ?? {} })),
          setRaw: (raw) => receive(() => this.#evaluation.setRaw(raw)),
        },
        this.#options.streaming ?? true,
        this.#options.pollingInterval ?? defaultPollingInterval,
        controller.signal,
      )
      .catch((e: unknown) => {
        // identify or stop aborted this evaluation and may have started another.
        if (controller.signal.aborted) return;
        // Defensive: values are only ever shown for the current context, so
        // stored or bootstrap values for it stay in use.
        if (this.#shown && this.#shown.contextKey !== this.#contextKey) this.#clear();
        throw e;
      });
    this.#running = { controller, started };
    return started;
  }
}

function isPromise<T>(value: T | Promise<T>): value is Promise<T> {
  return typeof (value as { then?: unknown } | null | undefined)?.then === 'function';
}
