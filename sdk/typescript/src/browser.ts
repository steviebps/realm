import { typedValue } from './evaluate.js';
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
  readonly #listeners = new Set<() => void>();
  #context: EvaluationContext;
  #flags: EvaluatedFlags = {};
  #raw = '';
  #controller: AbortController | undefined;
  #started = false;

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
    if (this.#started) return;
    this.#started = true;
    try {
      await this.#connect();
    } catch (e) {
      this.#started = false;
      throw e;
    }
  }

  /** Switches to a new context and re-evaluates every flag for it. */
  async identify(context: EvaluationContext): Promise<void> {
    this.#context = context;
    if (this.#started) await this.#connect();
  }

  /** Stops refreshing. The last known values stay available. */
  stop(): void {
    this.#started = false;
    this.#controller?.abort();
    this.#controller = undefined;
  }

  /** Calls listener whenever any flag's value changes. Returns a function that unsubscribes. */
  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
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
    return this.#flags;
  }

  #typed<T>(key: string, want: FlagType, fallback: T): T {
    const flag = this.#flags[key];
    return flag ? typedValue(flag.type, flag.value, want, fallback) : fallback;
  }

  // connect evaluates for the current context and replaces any refresh in
  // progress for an earlier one, so results for an old context are dropped.
  async #connect(): Promise<void> {
    this.#controller?.abort();
    const controller = new AbortController();
    this.#controller = controller;
    const { signal } = controller;
    const init: RequestInit = { method: 'POST', body: JSON.stringify(this.#context) };
    const apply = (e: Evaluation) => {
      if (!signal.aborted) this.#set(e.flags);
    };
    const load = async () => apply(await this.#transport.request<Evaluation>('evaluate', init, signal));

    await load();
    if (signal.aborted) return;
    if (this.#options.streaming ?? true) {
      void this.#transport.watch('evaluate', init, 'flags', (data) => apply(JSON.parse(data) as Evaluation), signal);
    } else {
      this.#transport.poll(load, this.#options.pollingInterval ?? defaultPollingInterval, signal);
    }
  }

  #set(flags: EvaluatedFlags): void {
    const raw = JSON.stringify(flags);
    if (raw === this.#raw) return;
    this.#flags = flags ?? {};
    this.#raw = raw;
    for (const listener of this.#listeners) listener();
  }
}
