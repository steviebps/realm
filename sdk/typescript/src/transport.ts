import { isRecord } from './snapshot.js';
import { readEvents } from './sse.js';

/** Options shared by both clients. */
export interface ClientOptions {
  /** The realm server, for example `https://realm.example.com`. */
  address: string;
  /** An API key, sent as a bearer token. */
  token?: string;
  /** The chamber to read, for example `/prod/checkout/`. Defaults to `/`. */
  path?: string;
  /**
   * Keep values current with a server-sent event stream (the default), or
   * poll. While the stream keeps failing, the client polls as well.
   */
  streaming?: boolean;
  /**
   * How often to poll, in milliseconds, when streaming is off or while the
   * stream keeps failing. Each wait varies by up to a tenth either way, at
   * random.
   */
  pollingInterval?: number;
  /**
   * How long, in milliseconds, to wait for a response (for an event stream,
   * until its headers arrive) before giving up and retrying. Defaults to
   * 10 seconds; 0 waits forever.
   */
  requestTimeout?: number;
  /**
   * Called when a refresh fails; the last known values stay in use. A
   * failure that repeats the previous one is not reported again until a
   * refresh succeeds. Defaults to console.warn, which also notes the
   * recovery with console.info.
   */
  onError?: (error: Error) => void;
  /** The fetch implementation; defaults to the global fetch. */
  fetch?: typeof fetch;
}

/**
 * Where a Transport delivers the data it follows; a Snapshot is one. Both
 * methods throw when the data is not what the route serves (a gateway's or
 * maintenance page's `{}`, say), which the Transport treats as a failed
 * refresh that keeps the last data.
 */
export interface Sink {
  /**
   * Receives already-parsed data, from a request, with the server's revision
   * of it (see parseRevision), if the server reported one.
   */
  set(data: unknown, revision: number | undefined): void;
  /** Receives JSON text, from an event stream, with its revision as set does. */
  setRaw(raw: string, revision: number | undefined): void;
}

/** The response header carrying the server's storage revision of a read. */
const revisionHeader = 'X-Realm-Revision';

/**
 * Parses a revision the server sent (its revision header, or an event's id):
 * a positive integer, or undefined when there is none or it is not one.
 * Revisions JavaScript cannot represent exactly are treated as missing.
 */
export function parseRevision(value: string | null | undefined): number | undefined {
  if (!value || !/^\d+$/.test(value.trim())) return undefined;
  const revision = Number(value.trim());
  return Number.isSafeInteger(revision) && revision > 0 ? revision : undefined;
}

/** A route a Transport keeps current; see Transport.follow. */
export interface Following {
  /** Settles when the first load does, rejecting if it failed. */
  readonly started: Promise<void>;
  /**
   * Settles with the current state: as started does until the first load
   * settles; after that, resolved once any data has reached the sink, and
   * otherwise rejected with the latest failure.
   */
  current(): Promise<void>;
}

/** An error from the realm server, with its HTTP status. */
export class RealmError extends Error {
  readonly status: number | undefined;
  /**
   * How long, in milliseconds, the server asked clients to wait before
   * retrying (the Retry-After header of a 429 or 503 response), if it did.
   */
  readonly retryAfter: number | undefined;

  constructor(message: string, status?: number, retryAfter?: number) {
    super(message);
    this.name = 'RealmError';
    this.status = status;
    this.retryAfter = retryAfter;
  }
}

const initialBackoff = 1_000;
const maxBackoff = 30_000;
// The longest backoff after a refusal (401 or 403: a bad or revoked key) or a
// 404 (no such chamber), which retrying soon would not fix. It also bounds
// the wait a Retry-After header can ask for.
const maxRefusedBackoff = 5 * 60_000;
// The server sends a heartbeat every 30 seconds.
const idleTimeout = 75_000;
const defaultRequestTimeout = 10_000;
// A stream that goes this many attempts in a row, or this long, without
// delivering an event is helped by polling until it delivers one again.
const fallbackAttempts = 3;
const fallbackDelay = 60_000;

// The state the refresh loops of one follow share.
interface FollowState {
  // Whether the first load has settled.
  settled: boolean;
  // Whether data has reached the sink.
  loaded: boolean;
  // Why the latest refresh failed, unless one has succeeded since.
  failure: Error | undefined;
}

// A refresh loop's record of how its refreshes went; see Transport.#monitor.
interface Monitor {
  failed(error: Error): void;
  succeeded(): void;
}

/** Talks to one chamber of a realm server. */
export class Transport {
  readonly #onError: (error: Error) => void;
  // Notes that refreshing works again after a reported failure.
  readonly #onRecover: () => void;
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
    if (options.onError) {
      this.#onError = options.onError;
      this.#onRecover = () => {};
    } else {
      this.#onError = (e) => console.warn('[realm]', e.message);
      this.#onRecover = () => console.info('[realm] recovered: refreshing works again');
    }
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
   * Reports error to onError. An onError that throws is reported to
   * console.error; it never reaches the caller, which may be a background
   * refresh that must carry on.
   */
  report(error: Error): void {
    try {
      this.#onError(error);
    } catch (e) {
      console.error('[realm] onError threw', e);
    }
  }

  // monitor returns a refresh loop's monitor, which records in state how its
  // refreshes went. It reports a failure to onError unless it repeats the
  // loop's previous one (the same status and message), so that a refused key
  // is reported once rather than on every retry; a success after a reported
  // failure is noted, and failures are reported again after it.
  #monitor(state: FollowState): Monitor {
    let last: string | undefined;
    return {
      failed: (error) => {
        state.failure = error;
        const key = `${error.name} ${error instanceof RealmError ? error.status : ''} ${error.message}`;
        if (key === last) return;
        last = key;
        this.report(error);
      },
      succeeded: () => {
        state.loaded = true;
        state.failure = undefined;
        if (last === undefined) return;
        last = undefined;
        try {
          this.#onRecover();
        } catch {
          // console.info, replaced by something that throws: not worth stopping for
        }
      },
    };
  }

  /**
   * Performs a request and returns the data in the response envelope, or
   * undefined when there is none. It rejects when no complete response
   * arrives within the request timeout.
   */
  async request(route: 'chambers' | 'evaluate', init: RequestInit, signal?: AbortSignal): Promise<unknown> {
    return (await this.#request(route, init, signal)).data;
  }

  // request performs a request as request does, and also returns the
  // revision the server reported.
  async #request(
    route: 'chambers' | 'evaluate',
    init: RequestInit,
    signal?: AbortSignal,
  ): Promise<{ data: unknown; revision: number | undefined }> {
    const d = deadline(this.#timeout, signal);
    try {
      const res = await this.#fetch(this.url(route), this.#init(init, 'application/json', d.signal));
      const body: unknown = await res.json().catch(() => undefined);
      if (d.expired()) throw d.error();
      if (!res.ok) throw await errorFrom(res, body);
      // For example, a captive portal's login page.
      if (body === undefined) throw new RealmError('the server did not return JSON', res.status);
      return { data: isRecord(body) ? body.data : undefined, revision: parseRevision(res.headers.get(revisionHeader)) };
    } catch (e) {
      throw d.expired() ? d.error() : e;
    } finally {
      d.dispose();
    }
  }

  /**
   * Loads route into sink, then keeps it current until signal aborts: over
   * an event stream when streaming (polling every interval milliseconds as
   * well while the stream keeps failing), otherwise by polling. Its started
   * rejects when the first load fails, but refreshing carries on in the
   * background (an event stream starts with the full data), so the sink
   * fills once the server is reachable.
   */
  follow(
    route: 'chambers' | 'evaluate',
    init: RequestInit,
    event: string,
    sink: Sink,
    streaming: boolean,
    interval: number,
    signal: AbortSignal,
  ): Following {
    const state: FollowState = { settled: false, loaded: false, failure: undefined };
    const started = this.#follow(route, init, event, sink, streaming, interval, signal, state);
    return {
      started,
      current: () => {
        if (!state.settled) return started;
        return state.loaded ? Promise.resolve() : Promise.reject(state.failure);
      },
    };
  }

  async #follow(
    route: 'chambers' | 'evaluate',
    init: RequestInit,
    event: string,
    sink: Sink,
    streaming: boolean,
    interval: number,
    signal: AbortSignal,
    state: FollowState,
  ): Promise<void> {
    // load requests the data and delivers it, unless its signal aborted first.
    const load = async (signal: AbortSignal) => {
      const { data, revision } = await this.#request(route, init, signal);
      if (!signal.aborted) sink.set(data, revision);
    };
    let failure: Error | undefined;
    try {
      await load(signal);
      state.loaded = true;
    } catch (e) {
      failure = state.failure = asError(e);
    }
    state.settled = true;
    if (signal.aborted) return;
    const background = streaming
      ? this.#watch(route, init, event, sink, signal, state, failure, (polling) =>
          this.#poll(load, interval, polling, state, undefined, false),
        )
      : this.#poll(load, interval, signal, state, failure, true);
    // The loops report their own failures; this only keeps a bug in them
    // from becoming an unhandled rejection.
    background.catch((e: unknown) => this.report(asError(e)));
    if (failure) throw failure;
  }

  // watch keeps an event stream open until signal aborts, passing the data of
  // each event named event to sink. It reconnects with capped exponential
  // backoff and jitter, waiting at least as long as a 503's Retry-After asks;
  // after failure (why the first load failed), it backs off before the first
  // attempt too. In a browser, coming back online cuts a backoff short. An
  // event that cannot be used is reported, leaving the stream open. While the
  // stream goes several attempts in a row without delivering an event, it
  // runs poll as well, until one arrives.
  async #watch(
    route: 'chambers' | 'evaluate',
    init: RequestInit,
    event: string,
    sink: Sink,
    signal: AbortSignal,
    state: FollowState,
    failure: Error | undefined,
    poll: (signal: AbortSignal) => Promise<void>,
  ): Promise<void> {
    const backoff = new Backoff();
    const monitor = this.#monitor(state);
    // The fallback polling, while it runs.
    let polling: AbortController | undefined;
    const stopPolling = () => {
      polling?.abort();
      polling = undefined;
    };
    signal.addEventListener('abort', stopPolling, { once: true });
    // How many attempts in a row have applied no event, and since when.
    let failures = 0;
    let failingSince = Date.now();
    if (failure) await sleep(backoff.next(failure), signal, true);
    while (!signal.aborted) {
      let applied = false;
      let error: Error | undefined;
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
          // Close it: an unread body holds its connection open, and a browser
          // has only six per server over HTTP/1.1.
          res.body?.cancel().catch(() => {});
          throw new RealmError('the server did not return an event stream', res.status);
        }
        await readEvents(
          res.body,
          (ev) => {
            if (ev.event !== event || signal.aborted) return;
            try {
              sink.setRaw(ev.data, parseRevision(ev.id));
            } catch (e) {
              // The stream stays open, and the last data in use.
              const notJSON = e instanceof SyntaxError;
              monitor.failed(notJSON ? new RealmError('the server sent an event that is not JSON') : asError(e));
              return;
            }
            monitor.succeeded();
            if (applied) return;
            applied = true;
            failures = 0;
            backoff.reset();
            stopPolling();
          },
          idleTimeout,
        );
      } catch (e) {
        if (signal.aborted) return;
        error = asError(e);
        monitor.failed(error);
      } finally {
        d.dispose();
      }
      if (signal.aborted) return;
      if (applied) {
        failingSince = Date.now();
      } else {
        failures++;
        if (!polling && (failures >= fallbackAttempts || Date.now() - failingSince >= fallbackDelay)) {
          polling = new AbortController();
          poll(polling.signal).catch((e: unknown) => this.report(asError(e)));
        }
      }
      await sleep(backoff.next(error), signal, true);
    }
  }

  // poll calls load every interval milliseconds (give or take a tenth, at
  // random) until signal aborts. Until the sink has data (failure is why the
  // first load failed) and, when eager, after any failure, it retries
  // sooner, with backoff. As a failing stream's fallback it is not eager:
  // the stream retries on its own, so once the sink has data, polling keeps
  // to the interval.
  async #poll(
    load: (signal: AbortSignal) => Promise<void>,
    interval: number,
    signal: AbortSignal,
    state: FollowState,
    failure: Error | undefined,
    eager: boolean,
  ): Promise<void> {
    const backoff = new Backoff();
    const monitor = this.#monitor(state);
    let error = failure;
    let retrying = !state.loaded;
    while (!signal.aborted) {
      await sleep(retrying ? backoff.next(error, interval) : jitter(interval), signal, retrying);
      if (signal.aborted) return;
      try {
        await load(signal);
      } catch (e) {
        if (signal.aborted) return;
        error = asError(e);
        monitor.failed(error);
        retrying = eager || !state.loaded;
        continue;
      }
      if (signal.aborted) return;
      error = undefined;
      retrying = false;
      backoff.reset();
      monitor.succeeded();
    }
  }
}

/**
 * Capped exponential backoff with jitter, as in the Go SDK, so that clients
 * retrying after the same outage spread out instead of arriving together:
 * each delay is a random value in [d/2, d) for the current step d, which
 * doubles from a second up to 30 seconds or, after a refusal (401 or 403) or
 * a 404, which retrying soon would not fix, up to five minutes. A delay is
 * never shorter than the wait a Retry-After header asked for.
 */
export class Backoff {
  #step = initialBackoff;

  /**
   * The delay before retrying after error (the latest failure, if any): at
   * most cap milliseconds, unless Retry-After asks for more.
   */
  next(error?: Error, cap = Infinity): number {
    const d = Math.min(this.#step, refused(error) ? maxRefusedBackoff : maxBackoff, cap);
    this.#step = Math.min(this.#step * 2, maxRefusedBackoff);
    const retryAfter = error instanceof RealmError ? (error.retryAfter ?? 0) : 0;
    return Math.max(d / 2 + Math.random() * (d / 2), retryAfter);
  }

  /** Starts over from the shortest delay, after a success. */
  reset(): void {
    this.#step = initialBackoff;
  }
}

// refused reports whether error is one that retrying soon would not fix: the
// server refused the key (401 or 403) or has no such chamber (404).
function refused(error: Error | undefined): boolean {
  const status = error instanceof RealmError ? error.status : undefined;
  return status === 401 || status === 403 || status === 404;
}

/**
 * Spreads interval by up to a tenth either way, at random, as the Go SDK
 * does, so that clients polling on the same interval drift apart.
 */
export function jitter(interval: number): number {
  return interval * (0.9 + Math.random() * 0.2);
}

/**
 * Returns interval when it is a usable polling interval (a positive number of
 * milliseconds), and otherwise fallback: polling without waiting would flood
 * the server.
 */
export function pollingInterval(interval: number | undefined, fallback: number): number {
  return typeof interval === 'number' && interval > 0 ? interval : fallback;
}

async function errorFrom(res: Response, body?: unknown): Promise<RealmError> {
  if (body === undefined) body = await res.json().catch(() => undefined);
  const errors = isRecord(body) ? body.errors : undefined;
  const message = (Array.isArray(errors) ? errors.join('; ') : '') || `${res.status} ${res.statusText}`.trim();
  return new RealmError(message, res.status, retryAfter(res));
}

// retryAfter returns how long, in milliseconds, a 429 or 503 response asks
// clients to wait before retrying (its Retry-After header, in seconds or as a
// date), at most maxRefusedBackoff, or undefined when it does not ask.
function retryAfter(res: Response): number | undefined {
  if (res.status !== 429 && res.status !== 503) return undefined;
  const value = res.headers.get('Retry-After')?.trim();
  if (!value) return undefined;
  const ms = /^\d+$/.test(value) ? Number(value) * 1000 : Date.parse(value) - Date.now();
  return Number.isNaN(ms) ? undefined : Math.min(Math.max(ms, 0), maxRefusedBackoff);
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
// reconnects soon after the network is back: within a second, at random, so
// that the clients on a network that comes back do not all reconnect at once.
function sleep(ms: number, signal: AbortSignal, wakeOnline: boolean): Promise<void> {
  return new Promise((resolve) => {
    const target = globalThis as OnlineTarget;
    const online = wakeOnline && typeof target.addEventListener === 'function';
    const end = Date.now() + ms;
    let timer = setTimeout(done, Math.min(ms, maxTimeout));
    signal.addEventListener('abort', done, { once: true });
    if (online) target.addEventListener!('online', wake);
    function wake() {
      target.removeEventListener?.('online', wake);
      clearTimeout(timer);
      timer = setTimeout(done, Math.min(Math.random() * initialBackoff, Math.max(end - Date.now(), 0)));
    }
    function done() {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      if (online) target.removeEventListener?.('online', wake);
      resolve();
    }
  });
}
