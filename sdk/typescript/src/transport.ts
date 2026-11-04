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
  /**
   * How long, in milliseconds, to wait for a response (for an event stream,
   * until its headers arrive) before giving up and retrying. Defaults to
   * 10 seconds; 0 waits forever.
   */
  requestTimeout?: number;
  /** Called when a refresh fails; the last known values stay in use. Defaults to console.warn. */
  onError?: (error: Error) => void;
  /** The fetch implementation; defaults to the global fetch. */
  fetch?: typeof fetch;
}

/** Where a Transport delivers the data it follows; a Snapshot is one. */
export interface Sink<T> {
  /** Receives already-parsed data, from a request. */
  set(value: T): void;
  /** Receives JSON text, from an event stream. */
  setRaw(raw: string): void;
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
const defaultRequestTimeout = 10_000;

/** Talks to one chamber of a realm server. */
export class Transport {
  readonly onError: (error: Error) => void;
  readonly #fetch: typeof fetch;
  readonly #base: URL;
  readonly #path: string;
  readonly #token: string | undefined;
  readonly #timeout: number;

  constructor(options: ClientOptions) {
    this.#base = new URL(options.address);
    this.#token = options.token;
    this.#timeout = options.requestTimeout ?? defaultRequestTimeout;
    this.#fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.onError = options.onError ?? ((e) => console.warn('[realm]', e.message));
    this.#path = (options.path ?? '/')
      .split('/')
      .filter((s) => s !== '')
      .map(encodeURIComponent)
      .join('/');
  }

  /** The URL of route for this chamber, with an optional query such as `?watch=true`. */
  url(route: 'chambers' | 'evaluate', query = ''): string {
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

  /**
   * Performs a request and returns the data in the response envelope. It
   * rejects when no complete response arrives within the request timeout.
   */
  async request<T>(route: 'chambers' | 'evaluate', init: RequestInit, signal?: AbortSignal): Promise<T> {
    const d = deadline(this.#timeout, signal);
    try {
      const res = await this.#fetch(this.url(route), this.#init(init, 'application/json', d.signal));
      const body = (await res.json().catch(() => undefined)) as { data?: T; errors?: string[] } | undefined;
      if (d.expired()) throw d.error();
      if (!res.ok) throw await errorFrom(res, body);
      // For example, a captive portal's login page.
      if (body === undefined) throw new RealmError('the server did not return JSON', res.status);
      return body.data as T;
    } catch (e) {
      throw d.expired() ? d.error() : e;
    } finally {
      d.dispose();
    }
  }

  /**
   * Keeps an event stream open until signal aborts, calling onData with the
   * data of each event named event. It reconnects with capped exponential
   * backoff, reporting failures to onError; with retrying, it backs off
   * before the first attempt too. In a browser, coming back online cuts a
   * backoff short.
   */
  async watch(
    route: 'chambers' | 'evaluate',
    init: RequestInit,
    event: string,
    onData: (data: string) => void,
    signal: AbortSignal,
    retrying = false,
  ): Promise<void> {
    let backoff = initialBackoff;
    if (retrying) {
      await sleep(backoff, signal, true);
      backoff = Math.min(backoff * 2, maxBackoff);
    }
    while (!signal.aborted) {
      // The deadline covers connecting; once the headers arrive, the idle
      // timeout watches the stream instead.
      const d = deadline(this.#timeout, signal);
      try {
        let res: Response;
        try {
          res = await this.#fetch(this.url(route, '?watch=true'), this.#init(init, 'text/event-stream', d.signal));
          if (!res.ok) throw await errorFrom(res);
        } catch (e) {
          throw d.expired() ? d.error() : e;
        } finally {
          d.clear();
        }
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
      } finally {
        d.dispose();
      }
      await sleep(backoff, signal, true);
      backoff = Math.min(backoff * 2, maxBackoff);
    }
  }

  /**
   * Loads route into sink, then keeps it current until signal aborts: over
   * an event stream when streaming, otherwise by polling every interval
   * milliseconds. It rejects when the first load fails, but keeps retrying
   * in the background (an event stream starts with the full data), so the
   * sink fills once the server is reachable.
   */
  async follow<T>(
    route: 'chambers' | 'evaluate',
    init: RequestInit,
    event: string,
    sink: Sink<T>,
    streaming: boolean,
    interval: number,
    signal: AbortSignal,
  ): Promise<void> {
    const load = async () => {
      const data = await this.request<T>(route, init, signal);
      if (!signal.aborted) sink.set(data);
    };
    let failure: Error | undefined;
    try {
      await load();
    } catch (e) {
      failure = asError(e);
    }
    if (signal.aborted) return;
    if (streaming) {
      void this.watch(route, init, event, (raw) => sink.setRaw(raw), signal, failure !== undefined);
    } else {
      void this.poll(load, interval, signal, failure !== undefined);
    }
    if (failure) throw failure;
  }

  /**
   * Calls refresh every interval milliseconds until signal aborts. After a
   * failure (or with retrying, from the start) it retries sooner, with
   * capped exponential backoff, until refresh succeeds again.
   */
  async poll(refresh: () => Promise<void>, interval: number, signal: AbortSignal, retrying = false): Promise<void> {
    let backoff = initialBackoff;
    while (!signal.aborted) {
      if (retrying) {
        await sleep(Math.min(backoff, interval), signal, true);
        backoff = Math.min(backoff * 2, maxBackoff);
      } else {
        await sleep(interval, signal, false);
      }
      if (signal.aborted) return;
      try {
        await refresh();
        retrying = false;
        backoff = initialBackoff;
      } catch (e) {
        if (signal.aborted) return;
        this.onError(asError(e));
        retrying = true;
      }
    }
  }
}

async function errorFrom(res: Response, body?: { errors?: string[] }): Promise<RealmError> {
  if (body === undefined) body = (await res.json().catch(() => undefined)) as { errors?: string[] } | undefined;
  const message = body?.errors?.join('; ') || `${res.status} ${res.statusText}`.trim();
  return new RealmError(message, res.status);
}

export function asError(e: unknown): Error {
  return e instanceof Error ? e : new Error(String(e));
}

// The largest delay setTimeout supports.
const maxTimeout = 2_147_483_647;

interface Deadline {
  /** Aborts when the parent signal does, or when time runs out. */
  signal: AbortSignal;
  /** Whether time ran out. */
  expired(): boolean;
  /** The error to report when time ran out. */
  error(): RealmError;
  /** Stops the clock; the signal still follows its parent. */
  clear(): void;
  /** Stops the clock and detaches from the parent signal. */
  dispose(): void;
}

// deadline returns a signal that aborts with parent or after ms milliseconds
// (never, when ms is 0). It is AbortSignal.any with AbortSignal.timeout, which
// older browsers lack, plus a clock that can be stopped.
function deadline(ms: number, parent?: AbortSignal): Deadline {
  const controller = new AbortController();
  let expired = false;
  const error = () => new RealmError(`the realm server did not respond within ${ms} ms`);
  const follow = () => controller.abort(parent?.reason);
  if (parent?.aborted) follow();
  else parent?.addEventListener('abort', follow, { once: true });
  const timer =
    ms > 0 && ms <= maxTimeout
      ? setTimeout(() => {
          expired = true;
          controller.abort(error());
        }, ms)
      : undefined;
  const clear = () => clearTimeout(timer);
  return {
    signal: controller.signal,
    expired: () => expired,
    error,
    clear,
    dispose() {
      clear();
      parent?.removeEventListener('abort', follow);
    },
  };
}

interface OnlineTarget {
  addEventListener?: (type: 'online', listener: () => void) => void;
  removeEventListener?: (type: 'online', listener: () => void) => void;
}

// sleep waits ms milliseconds, or until signal aborts. With wakeOnline, a
// browser's online event ends it early, so a client that was offline
// reconnects as soon as the network is back.
function sleep(ms: number, signal: AbortSignal, wakeOnline: boolean): Promise<void> {
  return new Promise((resolve) => {
    const target = globalThis as OnlineTarget;
    const online = wakeOnline && typeof target.addEventListener === 'function';
    const timer = setTimeout(done, Math.min(ms, maxTimeout));
    signal.addEventListener('abort', done, { once: true });
    if (online) target.addEventListener!('online', done);
    function done() {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      if (online) target.removeEventListener?.('online', done);
      resolve();
    }
  });
}
