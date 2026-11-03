import { evaluateChamber, flagValue, typedValue, VERSION_ATTRIBUTE } from './evaluate.js';
import { Snapshot } from './snapshot.js';
import { Transport, type ClientOptions } from './transport.js';
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
  readonly #chamber = new Snapshot<Chamber>({ rules: {} });
  #controller: AbortController | undefined;

  constructor(options: ServerClientOptions) {
    this.#options = options;
    this.#transport = new Transport(options);
    this.#defaults = { ...options.attributes };
    if (options.version) this.#defaults[VERSION_ATTRIBUTE] = options.version;
  }

  /**
   * Loads the chamber, then keeps it current in the background. It rejects
   * when the first load fails (for example, a bad key or a missing chamber).
   */
  async start(): Promise<void> {
    if (this.#controller) return;
    const controller = new AbortController();
    this.#controller = controller;
    try {
      await this.#transport.follow(
        'chambers',
        { method: 'GET' },
        'chamber',
        this.#chamber,
        this.#options.streaming ?? true,
        this.#options.pollingInterval ?? defaultPollingInterval,
        controller.signal,
      );
    } catch (e) {
      // A stop aborted this load; the client may have been started again since.
      if (controller.signal.aborted) return;
      this.#controller = undefined;
      throw e;
    }
  }

  /** Stops refreshing. The last known values stay available. */
  stop(): void {
    this.#controller?.abort();
    this.#controller = undefined;
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

  /** The value of a custom (JSON) flag for context, or fallback. */
  json<T>(flag: string, context: EvaluationContext | undefined, fallback: T): T {
    return this.#typed(flag, context, 'custom', fallback);
  }

  /** Every flag's value for context, for example to bootstrap a page. */
  allFlags(context: EvaluationContext = {}): EvaluatedFlags {
    return evaluateChamber(this.#chamber.value, context, this.#defaults);
  }

  #typed<T>(key: string, context: EvaluationContext = {}, want: FlagType, fallback: T): T {
    const flag = this.#chamber.value.rules?.[key];
    if (!flag) return fallback;
    return typedValue(flag.type, flagValue(key, flag, context, this.#defaults), want, fallback);
  }
}
