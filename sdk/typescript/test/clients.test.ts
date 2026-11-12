import type { ServerResponse } from 'node:http';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BrowserClient, RealmError, ServerClient, type EvaluationContext } from '../src/index.js';
import { fakeRealm, type FakeRequest } from './fake-realm.js';

const betaOn = { rules: { beta: { type: 'boolean', value: true } } };

const isWatch = (req: { url: string }) => req.url.endsWith('?watch=true');

// answer answers a request with status and body, as a gateway, proxy, or
// maintenance page in front of the server might.
function answer(res: ServerResponse, status: number, body: string, headers: Record<string, string> = {}) {
  res.writeHead(status, { 'Content-Type': 'application/json', ...headers });
  res.end(body);
  return true;
}

// shortestWaits makes every backoff and jitter take its shortest value, so
// tests that wait for retries are quick and their timing exact.
function shortestWaits() {
  const random = vi.spyOn(Math, 'random').mockReturnValue(0);
  cleanup.push(() => random.mockRestore());
}

// onlineEvents stands in for window's online event, returning the target to
// dispatch it on and the listeners registered.
function onlineEvents() {
  const target = new EventTarget();
  const listeners = new Set<unknown>();
  vi.stubGlobal('addEventListener', (type: string, fn: EventListener) => {
    listeners.add(fn);
    target.addEventListener(type, fn);
  });
  vi.stubGlobal('removeEventListener', (type: string, fn: EventListener) => {
    listeners.delete(fn);
    target.removeEventListener(type, fn);
  });
  cleanup.push(() => vi.unstubAllGlobals());
  return { target, listeners };
}

let cleanup: (() => unknown)[] = [];
afterEach(async () => {
  for (const fn of cleanup.reverse()) await fn();
  cleanup = [];
});

async function setup() {
  const realm = await fakeRealm();
  cleanup.push(realm.close);
  return realm;
}

describe('ServerClient', () => {
  it('loads the chamber, evaluates locally, and follows the stream', async () => {
    const realm = await setup();
    const client = new ServerClient({ address: realm.address, token: 'realm_a_b', path: '/prod/web' });
    cleanup.push(() => client.stop());
    await client.start();

    expect(client.bool('beta', { key: 'u1' }, true)).toBe(false);
    expect(client.string('beta', undefined, 'fallback')).toBe('fallback'); // wrong type
    expect(client.bool('missing', undefined, true)).toBe(true);
    expect(realm.requests[0]).toMatchObject({ method: 'GET', url: '/v1/chambers/prod/web/', auth: 'Bearer realm_a_b' });

    const changed = vi.fn();
    client.subscribe(changed);
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true));
    expect(changed).toHaveBeenCalledTimes(1);
  });

  it('reconnects when the stream drops', async () => {
    const realm = await setup();
    const client = new ServerClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await client.start();
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));

    realm.dropStreams();
    await vi.waitFor(() => expect(realm.streams.size).toBe(0));
    await vi.waitFor(() => expect(realm.streams.size).toBe(1), { timeout: 3000 });
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true));
  });

  it('rejects start when the server refuses', async () => {
    const realm = await setup();
    realm.setStatus(403);
    const client = new ServerClient({ address: realm.address, onError: () => {} });
    // a rejected start keeps retrying in the background until stopped
    cleanup.push(() => client.stop());
    const err = await client.start().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(RealmError);
    expect(err).toMatchObject({ status: 403, message: 'not allowed' });
  });

  it('stops a stream started again before the first load finished', async () => {
    const realm = await setup();
    const client = new ServerClient({ address: realm.address });
    cleanup.push(() => client.stop());
    const first = client.start();
    client.stop();
    await client.start();
    await expect(first).resolves.toBeUndefined();
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));

    client.stop();
    await vi.waitFor(() => expect(realm.streams.size).toBe(0));
  });

  it('keeps retrying the stream after a failed first load', async () => {
    const realm = await setup();
    realm.setStatus(503);
    const client = new ServerClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toMatchObject({ status: 503 });

    realm.setStatus(200);
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true), { timeout: 3000 });
    expect(realm.streams.size).toBe(1);
  });

  it('keeps retrying the poll after a failed first load', async () => {
    const realm = await setup();
    realm.setStatus(503);
    const client = new ServerClient({ address: realm.address, streaming: false, pollingInterval: 60_000, onError: () => {} });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toMatchObject({ status: 503 });

    realm.setStatus(200);
    realm.publish(betaOn);
    // well before the polling interval
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true), { timeout: 3000 });
  });

  it('returns the first load to a start called again while it is in flight', async () => {
    const realm = await setup();
    realm.publish(betaOn);
    const client = new ServerClient({ address: realm.address });
    cleanup.push(() => client.stop());
    const first = client.start();
    await client.start();
    expect(client.bool('beta', {}, false)).toBe(true);
    await first;

    client.stop();
    realm.setStatus(403);
    const failed = client.start();
    await expect(client.start()).rejects.toMatchObject({ status: 403 });
    await expect(failed).rejects.toMatchObject({ status: 403 });
  });

  it('times out requests and stream connects that hang', async () => {
    const realm = await setup();
    realm.setHang(true);
    const onError = vi.fn();
    const client = new ServerClient({ address: realm.address, requestTimeout: 100, onError });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toThrow('the realm server did not respond within 100 ms');

    // the stream's connect, retried after a backoff, times out too
    await vi.waitFor(() => expect(onError).toHaveBeenCalled(), { timeout: 3000 });
    expect(onError.mock.calls[0]?.[0]).toMatchObject({ message: 'the realm server did not respond within 100 ms' });

    realm.setHang(false);
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true), { timeout: 5000 });
  });

  it('polls when streaming is off', async () => {
    const realm = await setup();
    const client = new ServerClient({ address: realm.address, streaming: false, pollingInterval: 20 });
    cleanup.push(() => client.stop());
    await client.start();
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true));
    expect(realm.streams.size).toBe(0);
  });
});

describe('BrowserClient', () => {
  it('evaluates on the server, streams changes, and re-evaluates on identify', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, token: 'realm_c_d', path: 'prod/web/', context: { key: 'u1' } });
    cleanup.push(() => client.stop());
    await client.start();

    expect(client.bool('beta', true)).toBe(false);
    expect(realm.requests[0]).toMatchObject({
      method: 'POST',
      url: '/v1/evaluate/prod/web/',
      auth: 'Bearer realm_c_d',
      body: '{"key":"u1"}',
    });

    const changed = vi.fn();
    client.subscribe(changed);
    await client.identify({ key: 'vip' });
    expect(client.bool('beta', false)).toBe(true);
    // the stream for the old context is closed; one remains for "vip"
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    expect(realm.requests.at(-1)?.body).toBe('{"key":"vip"}');

    await client.identify({ key: 'u1' });
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', false)).toBe(true));
    // three value changes, plus readiness dropping on each identify
    expect(changed).toHaveBeenCalledTimes(5);
  });

  it('stops a stream started again before the first evaluation finished', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address });
    cleanup.push(() => client.stop());
    const first = client.start();
    client.stop();
    await client.start();
    await expect(first).resolves.toBeUndefined();
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));

    client.stop();
    await vi.waitFor(() => expect(realm.streams.size).toBe(0));
  });

  it('serves fallbacks, not the previous context, when identify fails, and keeps retrying', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, context: { key: 'vip' }, onError: () => {} });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.isReady()).toBe(true);
    expect(client.bool('beta', false)).toBe(true);

    realm.setStatus(403);
    await expect(client.identify({ key: 'u1' })).rejects.toMatchObject({ status: 403 });
    expect(client.isReady()).toBe(false);
    expect(client.allFlags()).toEqual({});
    expect(client.bool('beta', false)).toBe(false);

    realm.setStatus(200);
    await vi.waitFor(() => expect(client.isReady()).toBe(true), { timeout: 3000 });
    expect(client.allFlags()).toEqual({ beta: { type: 'boolean', value: false } });
    expect(realm.requests.at(-1)?.body).toBe('{"key":"u1"}');
  });

  it('never serves the previous context while the new evaluation is pending', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, context: { key: 'vip' }, onError: () => {} });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.bool('beta', false)).toBe(true);

    realm.setHang(true);
    const pending = client.identify({ key: 'u1' });
    // synchronously after identify: fallbacks, not vip's values
    expect(client.isReady()).toBe(false);
    expect(client.bool('beta', false)).toBe(false);
    realm.setHang(false);
    client.stop();
    await pending.catch(() => {});
  });

  it('keeps last known values for the same context when a restart fails', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, context: { key: 'vip' }, onError: () => {} });
    cleanup.push(() => client.stop());
    await client.start();
    client.stop();

    realm.setStatus(503);
    await expect(client.start()).rejects.toMatchObject({ status: 503 });
    expect(client.isReady()).toBe(true);
    expect(client.bool('beta', false)).toBe(true);
  });

  it('returns the first evaluation to a start called again while it is in flight', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, context: { key: 'vip' } });
    cleanup.push(() => client.stop());
    void client.start();
    await client.start();
    expect(client.isReady()).toBe(true);
    expect(client.bool('beta', false)).toBe(true);
  });

  it('isolates a subscriber that throws', async () => {
    const realm = await setup();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    cleanup.push(() => consoleError.mockRestore());
    const client = new BrowserClient({ address: realm.address });
    cleanup.push(() => client.stop());
    await client.start();
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));

    client.subscribe(() => {
      throw new Error('boom');
    });
    const changed = vi.fn();
    client.subscribe(changed);
    realm.publish(betaOn);
    await vi.waitFor(() => expect(changed).toHaveBeenCalledTimes(1));
    expect(client.bool('beta', false)).toBe(true);
    expect(consoleError).toHaveBeenCalledWith('[realm] a subscriber threw', expect.objectContaining({ message: 'boom' }));

    // the stream survived: another change arrives without a reconnect
    realm.publish({ rules: { beta: { type: 'boolean', value: false } } });
    await vi.waitFor(() => expect(changed).toHaveBeenCalledTimes(2));
    expect(realm.requests.filter((r) => r.url.endsWith('?watch=true'))).toHaveLength(1);
  });

  it('reconnects as soon as the browser comes back online', async () => {
    shortestWaits();
    const realm = await setup();
    const { target, listeners } = onlineEvents();

    realm.setStatus(503);
    const client = new BrowserClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toMatchObject({ status: 503 });
    // the first retry fails too; the next waits a second
    await vi.waitFor(() => expect(realm.requests).toHaveLength(2), { timeout: 3000 });
    await vi.waitFor(() => expect(listeners.size).toBe(1));

    realm.setStatus(200);
    target.dispatchEvent(new Event('online'));
    await vi.waitFor(() => expect(client.isReady()).toBe(true), { timeout: 500 });
    expect(listeners.size).toBe(0);

    // stop removes the listener of a client waiting to retry
    realm.setStatus(503);
    const other = new BrowserClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => other.stop());
    await expect(other.start()).rejects.toMatchObject({ status: 503 });
    await vi.waitFor(() => expect(listeners.size).toBe(1));
    other.stop();
    expect(listeners.size).toBe(0);
  });

  it('spreads out reconnecting when the browser comes back online', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5);
    cleanup.push(() => vi.mocked(Math.random).mockRestore());
    const realm = await setup();
    const { target, listeners } = onlineEvents();

    realm.setStatus(503);
    const client = new BrowserClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toMatchObject({ status: 503 });
    // the first retry fails too; the next waits a second and a half
    await vi.waitFor(() => expect(realm.requests).toHaveLength(2), { timeout: 3000 });
    await vi.waitFor(() => expect(listeners.size).toBe(1));

    realm.setStatus(200);
    const online = Date.now();
    target.dispatchEvent(new Event('online'));
    await vi.waitFor(() => expect(client.isReady()).toBe(true), { timeout: 2000 });
    // half of the second it may wait after the event, not at once or after the backoff
    expect(realm.requests.at(-1)!.at - online).toBeGreaterThanOrEqual(450);
    expect(realm.requests.at(-1)!.at - online).toBeLessThan(1200);
  });

  it('identify before start only sets the context', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, streaming: false });
    cleanup.push(() => client.stop());
    await client.identify({ key: 'vip' });
    expect(realm.requests).toHaveLength(0);
    await client.start();
    expect(client.bool('beta', false)).toBe(true);
  });
});

describe('revision', () => {
  it('reports the ServerClient chamber revision, loaded and streamed', async () => {
    const realm = await setup();
    const client = new ServerClient({ address: realm.address });
    cleanup.push(() => client.stop());
    expect(client.revision()).toBeUndefined();
    realm.setRevision('41');
    await client.start();
    expect(client.revision()).toBe(41);

    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    realm.setRevision('42');
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.revision()).toBe(42));
    expect(client.bool('beta', {}, false)).toBe(true);

    // the same chamber at a newer revision updates only the revision
    const changed = vi.fn();
    client.subscribe(changed);
    realm.setRevision('43');
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.revision()).toBe(43));
    expect(changed).not.toHaveBeenCalled();

    // an event without one leaves the revision unknown
    realm.setRevision(undefined);
    realm.publish({ rules: {} });
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(false));
    expect(client.revision()).toBeUndefined();
  });

  it('reports the ServerClient revision when polling', async () => {
    const realm = await setup();
    realm.setRevision('7');
    const client = new ServerClient({ address: realm.address, streaming: false });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.revision()).toBe(7);
  });

  it('reports the BrowserClient revision only for values from the server', async () => {
    const realm = await setup();
    realm.setRevision('5');
    const client = new BrowserClient({ address: realm.address, context: { key: 'u1' }, bootstrap: { beta: { type: 'boolean', value: true } } });
    cleanup.push(() => client.stop());
    expect(client.source()).toBe('bootstrap');
    expect(client.revision()).toBeUndefined();
    await client.start();
    expect(client.revision()).toBe(5);

    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    realm.setRevision('6');
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.revision()).toBe(6));

    // until the new context's evaluation arrives, its values are not the server's
    realm.setHang(true);
    void client.identify({ key: 'u2' }).catch(() => {});
    expect(client.revision()).toBeUndefined();
  });
});

describe('a server answering without data', () => {
  it.each(['{}', '{"data":null}', '{"data":{"rules":null}}', '{"data":[]}', 'null'])(
    'keeps the last chamber after %s',
    async (body) => {
      const realm = await setup();
      realm.publish(betaOn);
      const onError = vi.fn();
      const client = new ServerClient({ address: realm.address, streaming: false, pollingInterval: 10, onError });
      cleanup.push(() => client.stop());
      await client.start();

      realm.intercept((_req, res) => answer(res, 200, body));
      await vi.waitFor(() => expect(onError).toHaveBeenCalled());
      expect(onError.mock.calls[0]?.[0]).toMatchObject({ message: 'the server did not return a chamber' });
      expect(client.isReady()).toBe(true);
      expect(client.bool('beta', {}, false)).toBe(true);
      expect(client.allFlags()).toEqual({ beta: { type: 'boolean', value: true } });
    },
  );

  it('rejects a first load without a chamber, and getters return their fallbacks', async () => {
    const realm = await setup();
    realm.intercept((_req, res) => answer(res, 200, '{}'));
    const client = new ServerClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toThrow('the server did not return a chamber');
    expect(client.isReady()).toBe(false);
    expect(client.bool('beta', {}, true)).toBe(true);
    expect(client.json('beta', {}, 'fallback')).toBe('fallback');
    expect(client.allFlags()).toEqual({});
  });

  it('keeps the last evaluation after a response without one', async () => {
    const realm = await setup();
    const onError = vi.fn();
    const options = { address: realm.address, context: { key: 'vip' }, streaming: false, pollingInterval: 10, onError };
    const client = new BrowserClient(options);
    cleanup.push(() => client.stop());
    await client.start();

    realm.intercept((_req, res) => answer(res, 200, '{"data":{"flags":null}}'));
    await vi.waitFor(() => expect(onError).toHaveBeenCalled());
    expect(onError.mock.calls[0]?.[0]).toMatchObject({ message: 'the server did not return an evaluation' });
    expect(client.isReady()).toBe(true);
    expect(client.bool('beta', false)).toBe(true);
    expect(client.allFlags()).toEqual({ beta: { type: 'boolean', value: true } });
  });

  it('returns fallbacks for flags of a shape it does not expect', async () => {
    const realm = await setup();
    realm.publish({ rules: { beta: { type: 'boolean', value: true, targeting: { not: 'a list' } }, other: 5 } });
    const client = new ServerClient({ address: realm.address, streaming: false });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.bool('beta', {}, false)).toBe(false);
    expect(client.bool('other', {}, false)).toBe(false);
    expect(client.allFlags()).toEqual({});
  });
});

describe('refreshing', () => {
  it.each([true, false])('carries on when onError throws (streaming: %s)', async (streaming) => {
    shortestWaits();
    const realm = await setup();
    const unhandled = vi.fn();
    process.on('unhandledRejection', unhandled);
    cleanup.push(() => process.off('unhandledRejection', unhandled));
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    cleanup.push(() => consoleError.mockRestore());

    realm.setStatus(503);
    const onError = vi.fn(() => {
      throw new Error('boom');
    });
    const client = new ServerClient({ address: realm.address, streaming, pollingInterval: 10, onError });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toMatchObject({ status: 503 });
    await vi.waitFor(() => expect(onError).toHaveBeenCalled(), { timeout: 3000 });

    realm.setStatus(200);
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true), { timeout: 5000 });
    expect(consoleError).toHaveBeenCalledWith('[realm] onError threw', expect.objectContaining({ message: 'boom' }));
    expect(unhandled).not.toHaveBeenCalled();
  });

  it('polls while the stream keeps failing, until it delivers again', async () => {
    shortestWaits();
    const realm = await setup();
    // a server at its cap on open streams: requests work, streams get 503
    const capped = (req: FakeRequest, res: ServerResponse) =>
      isWatch(req) && answer(res, 503, '{"errors":["too many open streams; try again later"]}');
    realm.intercept(capped);
    const onError = vi.fn();
    const client = new ServerClient({ address: realm.address, pollingInterval: 50, onError });
    cleanup.push(() => client.stop());
    await client.start();

    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true), { timeout: 5000 });
    expect(realm.requests.filter(isWatch).length).toBeGreaterThanOrEqual(3);
    // the stream's failures, once: the polls succeed
    expect(onError.mock.calls.map(([e]) => (e as Error).message)).toEqual(['too many open streams; try again later']);

    // once the stream delivers again (just after the server opens it), polling stops
    realm.intercept(undefined);
    await vi.waitFor(() => expect(realm.streams.size).toBe(1), { timeout: 5000 });
    await new Promise((r) => setTimeout(r, 100));
    const polls = () => realm.requests.filter((r) => !isWatch(r)).length;
    const before = polls();
    await new Promise((r) => setTimeout(r, 300));
    expect(polls()).toBe(before);
  }, 15_000);

  it('waits as long as a 503 asks before reconnecting the stream', async () => {
    const realm = await setup();
    realm.intercept((req, res) => isWatch(req) && answer(res, 503, '{"errors":["busy"]}', { 'Retry-After': '2' }));
    const client = new ServerClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await client.start();
    await vi.waitFor(() => expect(realm.requests.filter(isWatch)).toHaveLength(2), { timeout: 5000 });
    const [first, second] = realm.requests.filter(isWatch);
    // without Retry-After, the first retry comes within a second
    expect(second!.at - first!.at).toBeGreaterThanOrEqual(1900);
  });

  it('keeps the stream open, and the last values, when an event cannot be used', async () => {
    const realm = await setup();
    const onError = vi.fn();
    const client = new ServerClient({ address: realm.address, onError });
    cleanup.push(() => client.stop());
    await client.start();
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));

    realm.write('event: chamber\ndata: <html>down for maintenance</html>\n\n');
    realm.write('event: chamber\ndata: {"rules":null}\n\n');
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(2));
    expect(onError.mock.calls.map(([e]) => (e as Error).message)).toEqual([
      'the server sent an event that is not JSON',
      'the server did not return a chamber',
    ]);
    expect(client.bool('beta', {}, true)).toBe(false);

    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true));
    expect(realm.requests.filter(isWatch)).toHaveLength(1);
  });

  it('backs off a stream that sends only events it cannot use', async () => {
    shortestWaits();
    const realm = await setup();
    realm.intercept((req, res) => {
      if (!isWatch(req)) return false;
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      res.end('event: chamber\ndata: not json\n\n');
      return true;
    });
    const client = new ServerClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await client.start();
    await vi.waitFor(() => expect(realm.requests.filter(isWatch)).toHaveLength(3), { timeout: 5000 });
    const [a, b, c] = realm.requests.filter(isWatch).map((r) => r.at);
    // the wait doubled: the events did not reset the backoff
    expect(c! - b!).toBeGreaterThan(1.5 * (b! - a!));
  });

  it('closes a watch response that is not an event stream', async () => {
    const realm = await setup();
    let open = 0;
    realm.intercept((req, res) => {
      if (!isWatch(req)) return false;
      // something else, which never finishes
      open++;
      res.on('close', () => open--);
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.write('{"data":');
      return true;
    });
    const onError = vi.fn();
    const client = new ServerClient({ address: realm.address, onError });
    cleanup.push(() => client.stop());
    await client.start();
    await vi.waitFor(() => expect(realm.requests.filter(isWatch)).toHaveLength(2), { timeout: 3000 });
    await vi.waitFor(() => expect(open).toBe(0));
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: 'the server did not return an event stream' }));
  });

  it('reports a failure that repeats once, until a refresh succeeds', async () => {
    const realm = await setup();
    const onError = vi.fn();
    const client = new ServerClient({ address: realm.address, streaming: false, pollingInterval: 10, onError });
    cleanup.push(() => client.stop());
    await client.start();
    const failures = () => onError.mock.calls.map(([e]) => (e as RealmError).status);
    const retried = async () => {
      const seen = realm.requests.length;
      await vi.waitFor(() => expect(realm.requests.length).toBeGreaterThan(seen + 3));
    };

    realm.setStatus(403);
    await retried();
    expect(failures()).toEqual([403]);

    realm.setStatus(503);
    await retried();
    expect(failures()).toEqual([403, 503]);

    realm.setStatus(200);
    realm.publish(betaOn);
    await vi.waitFor(() => expect(client.bool('beta', {}, false)).toBe(true));
    realm.setStatus(503);
    await retried();
    expect(failures()).toEqual([403, 503, 503]);
  });

  it('warns once by default, and notes the recovery', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const info = vi.spyOn(console, 'info').mockImplementation(() => {});
    cleanup.push(() => warn.mockRestore());
    cleanup.push(() => info.mockRestore());
    const realm = await setup();
    const client = new ServerClient({ address: realm.address, streaming: false, pollingInterval: 10 });
    cleanup.push(() => client.stop());
    await client.start();

    realm.setStatus(403);
    const seen = realm.requests.length;
    await vi.waitFor(() => expect(realm.requests.length).toBeGreaterThan(seen + 3));
    expect(warn.mock.calls).toEqual([['[realm]', 'not allowed']]);
    expect(info).not.toHaveBeenCalled();

    realm.setStatus(200);
    await vi.waitFor(() => expect(info).toHaveBeenCalledWith('[realm] recovered: refreshing works again'));
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it('polls instead of flooding the server for an interval that is not positive', async () => {
    const realm = await setup();
    for (const pollingInterval of [0, -1, Number.NaN]) {
      const client = new ServerClient({ address: realm.address, streaming: false, pollingInterval });
      await client.start();
      const seen = realm.requests.length;
      await new Promise((r) => setTimeout(r, 100));
      client.stop();
      expect(realm.requests.length, String(pollingInterval)).toBe(seen);
    }
  });
});

describe('contexts from JavaScript', () => {
  // as a JavaScript caller might pass it, with values that are not strings
  const context = (key: unknown, attributes: Record<string, unknown>) => ({ key, attributes }) as unknown as EvaluationContext;

  it('match number and boolean attributes as strings, ignoring other values', async () => {
    const realm = await setup();
    realm.publish({
      rules: {
        beta: {
          type: 'boolean',
          value: false,
          targeting: [
            {
              value: true,
              clauses: [
                { attribute: 'key', op: 'in', values: ['42'] },
                { attribute: 'build', op: 'startsWith', values: ['12'] },
                { attribute: 'paid', op: 'in', values: ['true'] },
              ],
            },
          ],
        },
      },
    });
    const attributes = { paid: true } as unknown as Record<string, string>;
    const client = new ServerClient({ address: realm.address, streaming: false, attributes });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.bool('beta', context(42, { build: 123 }), false)).toBe(true);
    expect(client.bool('beta', context(42, { build: { major: 12 } }), false)).toBe(false);
    expect(client.allFlags(context(42, { build: 123, paid: false }))).toEqual({ beta: { type: 'boolean', value: false } });
  });

  it('are sent to the server with number and boolean values as strings', async () => {
    const realm = await setup();
    const ctx = context(42, { build: 123, paid: true, plan: 'pro', tags: ['a'], owner: null });
    const client = new BrowserClient({ address: realm.address, context: ctx, streaming: false });
    cleanup.push(() => client.stop());
    await client.start();
    expect(JSON.parse(realm.requests[0]!.body)).toEqual({ key: '42', attributes: { build: '123', paid: 'true', plan: 'pro' } });
    // still the context as given, to compare with
    expect(client.contextKey()).toBe(JSON.stringify(ctx));
  });
});

describe('json', () => {
  const quota = { rules: { quota: { type: 'custom', value: { max: 3, tiers: ['a'] } } } };

  it('returns a copy from a ServerClient', async () => {
    const realm = await setup();
    realm.publish(quota);
    const client = new ServerClient({ address: realm.address, streaming: false });
    cleanup.push(() => client.stop());
    await client.start();
    const value = client.json('quota', undefined, { max: 0, tiers: [] as string[] });
    value.max = 99;
    value.tiers.push('b');
    (client.allFlags().quota!.value as { max: number }).max = 98;
    expect(client.json('quota', undefined, {})).toEqual({ max: 3, tiers: ['a'] });
    const fallback = { max: 0 };
    expect(client.json('missing', undefined, fallback)).toBe(fallback);
  });

  it('returns a copy from a BrowserClient', async () => {
    const realm = await setup();
    realm.publish(quota);
    const client = new BrowserClient({ address: realm.address, streaming: false });
    cleanup.push(() => client.stop());
    await client.start();
    const value = client.json('quota', { max: 0, tiers: [] as string[] });
    value.max = 99;
    value.tiers.push('b');
    expect(client.json('quota', {})).toEqual({ max: 3, tiers: ['a'] });
    expect(client.allFlags().quota?.value).toEqual({ max: 3, tiers: ['a'] });
  });
});

describe('start', () => {
  it('reports ServerClient readiness, and a later start reflects the current state', async () => {
    const realm = await setup();
    realm.setStatus(503);
    const client = new ServerClient({ address: realm.address, streaming: false, pollingInterval: 10, onError: () => {} });
    cleanup.push(() => client.stop());
    expect(client.isReady()).toBe(false);
    await expect(client.start()).rejects.toMatchObject({ status: 503 });
    expect(client.isReady()).toBe(false);

    // still failing, now for another reason
    realm.setStatus(403);
    await vi.waitFor(() => expect(client.start()).rejects.toMatchObject({ status: 403 }));

    realm.setStatus(200);
    await vi.waitFor(() => expect(client.isReady()).toBe(true));
    await expect(client.start()).resolves.toBeUndefined();
    client.stop();
    expect(client.isReady()).toBe(true);
  });

  it('resolves a later BrowserClient start once an evaluation has arrived', async () => {
    const realm = await setup();
    realm.setStatus(503);
    const client = new BrowserClient({ address: realm.address, streaming: false, pollingInterval: 10, onError: () => {} });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toMatchObject({ status: 503 });
    await expect(client.start()).rejects.toMatchObject({ status: 503 });

    realm.setStatus(200);
    await vi.waitFor(() => expect(client.isReady()).toBe(true));
    await expect(client.start()).resolves.toBeUndefined();
  });
});
