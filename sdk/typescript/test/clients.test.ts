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
    const client = new ServerClient({ address: realm.address });
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
    expect(changed).toHaveBeenCalledTimes(3);
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
