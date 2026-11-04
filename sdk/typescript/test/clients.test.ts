import { afterEach, describe, expect, it, vi } from 'vitest';
import { BrowserClient, RealmError, ServerClient } from '../src/index.js';
import { fakeRealm } from './fake-realm.js';

const betaOn = { rules: { beta: { type: 'boolean', value: true } } };

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
    const realm = await setup();
    // a stand-in for window's online event
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

    realm.setStatus(503);
    const client = new BrowserClient({ address: realm.address, onError: () => {} });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toMatchObject({ status: 503 });
    // the first retry fails too; the next waits two seconds
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
