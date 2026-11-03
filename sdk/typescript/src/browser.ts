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
  #controller: AbortController | undefined;

  constructor(options: BrowserClientOptions) {
    this.#options = options;
    this.#transport = new Transport(options);
    this.#context = options.context ?? {};
  }

  /**
   * Evaluates flags for the context, then keeps them current in the
   * background. It rejects when the first evaluation fails.
   */
  async start(): Promise<void> {
    if (this.#controller) return;
    try {
      await this.#connect();
    } catch (e) {
      this.#controller = undefined;
      throw e;
    }
  }

  /** Switches to a new context and re-evaluates every flag for it. */
  async identify(context: EvaluationContext): Promise<void> {
    this.#context = context;
    if (this.#controller) await this.#connect();
  }

  /** Stops refreshing. The last known values stay available. */
  stop(): void {
    this.#controller?.abort();
    this.#controller = undefined;
  }

  /** Calls listener whenever any flag's value changes. Returns a function that unsubscribes. */
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
  async #connect(): Promise<void> {
    this.#controller?.abort();
    const controller = new AbortController();
    this.#controller = controller;
    await this.#transport.follow(
      'evaluate',
      { method: 'POST', body: JSON.stringify(this.#context) },
      'flags',
      this.#evaluation,
      this.#options.streaming ?? true,
      this.#options.pollingInterval ?? defaultPollingInterval,
      controller.signal,
    );
  }
}
