import { readEvents } from './sse.js';

/** Options shared by both clients. */
export interface ClientOptions {
  /** The realm server, for example `https://realm.example.com`. */
  address: string;
  /** An API key, sent as a bearer token. */
  token?: string;
  /** The chamber to read, for example `/prod/checkout/`. Defaults to `/`. */
  path?: string;
  /** Keep values current with a server-sent event stream (the default), or poll. */
  streaming?: boolean;
  /** How often to poll, in milliseconds, when streaming is off. */
  pollingInterval?: number;
  /** Called when a refresh fails; the last known values stay in use. Defaults to console.warn. */
  onError?: (error: Error) => void;
  /** The fetch implementation; defaults to the global fetch. */
  fetch?: typeof fetch;
}

/** An error from the realm server, with its HTTP status. */
export class RealmError extends Error {
  readonly status: number | undefined;

  constructor(message: string, status?: number) {
    super(message);
    this.name = 'RealmError';
    this.status = status;
  }
}

const initialBackoff = 1_000;
const maxBackoff = 30_000;
// The server sends a heartbeat every 30 seconds.
const idleTimeout = 75_000;

/** Talks to one chamber of a realm server. */
export class Transport {
  readonly onError: (error: Error) => void;
  readonly #fetch: typeof fetch;
  readonly #base: URL;
  readonly #path: string;
  readonly #token: string | undefined;

  constructor(options: ClientOptions) {
    this.#base = new URL(options.address);
    this.#token = options.token;
    this.#fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.onError = options.onError ?? ((e) => console.warn('[realm]', e.message));
    this.#path = (options.path ?? '/')
      .split('/')
      .filter((s) => s !== '')
      .map(encodeURIComponent)
      .join('/');
  }

  #url(route: 'chambers' | 'evaluate', query = ''): string {
    const path = this.#path === '' ? '' : this.#path + '/';
    return new URL(`/v1/${route}/${path}${query}`, this.#base).toString();
  }

  #init(init: RequestInit, accept: string, signal?: AbortSignal): RequestInit {
    const headers = new Headers(init.headers);
    headers.set('Accept', accept);
    if (this.#token) headers.set('Authorization', `Bearer ${this.#token}`);
    if (init.body !== undefined) headers.set('Content-Type', 'application/json');
    return { ...init, headers, signal };
  }

  /** Performs a request and returns the data in the response envelope. */
  async request<T>(route: 'chambers' | 'evaluate', init: RequestInit, signal?: AbortSignal): Promise<T> {
    const res = await this.#fetch(this.#url(route), this.#init(init, 'application/json', signal));
    const body = (await res.json().catch(() => undefined)) as { data?: T; errors?: string[] } | undefined;
    if (!res.ok) throw await errorFrom(res, body);
    return body?.data as T;
  }

  /**
   * Keeps an event stream open until signal aborts, calling onData with the
   * data of each event named event. It reconnects with capped exponential
   * backoff, reporting failures to onError.
   */
  async watch(
    route: 'chambers' | 'evaluate',
    init: RequestInit,
    event: string,
    onData: (data: string) => void,
    signal: AbortSignal,
  ): Promise<void> {
    let backoff = initialBackoff;
    while (!signal.aborted) {
      try {
        const res = await this.#fetch(this.#url(route, '?watch=true'), this.#init(init, 'text/event-stream', signal));
        if (!res.ok) throw await errorFrom(res);
        if (!res.headers.get('Content-Type')?.startsWith('text/event-stream') || !res.body) {
          throw new RealmError('the server did not return an event stream', res.status);
        }
        await readEvents(
          res.body,
          (ev) => {
            if (ev.event !== event || signal.aborted) return;
            backoff = initialBackoff;
            onData(ev.data);
          },
          idleTimeout,
        );
      } catch (e) {
        if (signal.aborted) return;
        this.onError(asError(e));
      }
      await sleep(backoff, signal);
      backoff = Math.min(backoff * 2, maxBackoff);
    }
  }

  /** Calls refresh every interval milliseconds until signal aborts. */
  poll(refresh: () => Promise<void>, interval: number, signal: AbortSignal): void {
    const timer = setInterval(() => {
      refresh().catch((e) => {
        if (!signal.aborted) this.onError(asError(e));
      });
    }, interval);
    signal.addEventListener('abort', () => clearInterval(timer), { once: true });
  }
}

async function errorFrom(res: Response, body?: { errors?: string[] }): Promise<RealmError> {
  if (body === undefined) body = (await res.json().catch(() => undefined)) as { errors?: string[] } | undefined;
  const message = body?.errors?.join('; ') || `${res.status} ${res.statusText}`.trim();
  return new RealmError(message, res.status);
}

function asError(e: unknown): Error {
  return e instanceof Error ? e : new Error(String(e));
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(done, ms);
    signal.addEventListener('abort', done, { once: true });
    function done() {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      resolve();
    }
  });
}
