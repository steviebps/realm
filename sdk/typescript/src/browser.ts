import { typedValue } from './evaluate.js';
import { Snapshot } from './snapshot.js';
import { Transport, type ClientOptions } from './transport.js';
import type { EvaluatedFlags, EvaluationContext, FlagType } from './types.js';

/** Options for a BrowserClient. */
export interface BrowserClientOptions extends ClientOptions {
  /** Who flags are evaluated for; change it later with identify. */
  context?: EvaluationContext;
}

const defaultPollingInterval = 5 * 60_000;

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
  readonly #evaluation = new Snapshot<Evaluation>({ flags: {} });
  #context: EvaluationContext;
  // The context as JSON, and the context the current values were evaluated
  // for (undefined before the first evaluation).
  #contextKey: string;
  #evaluatedFor: string | undefined;
  // The background refresh, and its first evaluation, while started.
  #running: { controller: AbortController; started: Promise<void> } | undefined;

  constructor(options: BrowserClientOptions) {
    this.#options = options;
    this.#transport = new Transport(options);
    this.#context = options.context ?? {};
    this.#contextKey = JSON.stringify(this.#context);
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
   * new evaluation arrives, isReady is false and getters return their
   * fallbacks: values evaluated for another context (another user on a
   * shared device, say) are never served for this one. The client keeps
   * retrying if the evaluation fails. On a stopped client it only sets the
   * context.
   */
  async identify(context: EvaluationContext): Promise<void> {
    const wasReady = this.isReady();
    this.#context = context;
    this.#contextKey = JSON.stringify(context);
    if (this.#evaluatedFor !== undefined && this.#evaluatedFor !== this.#contextKey) {
      this.#evaluatedFor = undefined;
      if (!this.#evaluation.set({ flags: {} }) && wasReady) this.#evaluation.notify();
    } else if (wasReady !== this.isReady()) {
      this.#evaluation.notify();
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
   * Until they are, getters return their fallbacks.
   */
  isReady(): boolean {
    return this.#evaluatedFor === this.#contextKey;
  }

  /**
   * Calls listener whenever any flag's value changes, or isReady does.
   * Returns a function that unsubscribes.
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
    return this.#evaluation.value.flags ?? {};
  }

  #typed<T>(key: string, want: FlagType, fallback: T): T {
    const flag = this.allFlags()[key];
    return flag ? typedValue(flag.type, flag.value, want, fallback) : fallback;
  }

  // connect evaluates for the current context, replacing any refresh for an
  // earlier one, whose results are then dropped.
  #connect(): Promise<void> {
    this.#running?.controller.abort();
    const controller = new AbortController();
    const key = this.#contextKey;
    // receive records that the values are now key's, then applies update,
    // telling listeners if that made the client ready without changing a value.
    const receive = (update: () => boolean) => {
      const wasReady = this.isReady();
      const before = this.#evaluatedFor;
      this.#evaluatedFor = key;
      let changed: boolean;
      try {
        changed = update();
      } catch (e) {
        this.#evaluatedFor = before;
        throw e;
      }
      if (!changed && !wasReady) this.#evaluation.notify();
    };
    const started = this.#transport
      .follow<Evaluation>(
        'evaluate',
        { method: 'POST', body: JSON.stringify(this.#context) },
        'flags',
        {
          set: (value) => receive(() => this.#evaluation.set(value)),
          setRaw: (raw) => receive(() => this.#evaluation.setRaw(raw)),
        },
        this.#options.streaming ?? true,
        this.#options.pollingInterval ?? defaultPollingInterval,
        controller.signal,
      )
      .catch((e: unknown) => {
        // identify or stop aborted this evaluation and may have started another.
        if (controller.signal.aborted) return;
        // The values left are another context's: serve fallbacks instead.
        if (!this.isReady()) {
          this.#evaluatedFor = undefined;
          this.#evaluation.set({ flags: {} });
        }
        throw e;
      });
    this.#running = { controller, started };
    return started;
  }
}
