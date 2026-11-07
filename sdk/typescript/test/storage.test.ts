import { afterEach, describe, expect, it, vi } from 'vitest';
import { BrowserClient, localStorageAdapter, type EvaluationContext, type StorageAdapter } from '../src/index.js';
import { decodeFlags, maxStoredContexts, recentKey, storageKey } from '../src/storage.js';
import { fakeRealm, seedStorage } from './fake-realm.js';

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

// memoryStorage is a synchronous StorageAdapter, like localStorage.
function memoryStorage() {
  const items = new Map<string, string>();
  return {
    items,
    getItem: (key: string) => items.get(key) ?? null,
    setItem: (key: string, value: string) => void items.set(key, value),
    removeItem: (key: string) => void items.delete(key),
  };
}

// entries returns the values items holds for contexts, without the list of
// the most recently used ones.
function entries(items: Map<string, string>): string[] {
  return [...items].filter(([key]) => !key.endsWith(':recent')).map(([, value]) => value);
}

// asyncStorage answers reads only when release is called, like IndexedDB on a busy device.
function asyncStorage(items: Map<string, string>) {
  const pending: (() => void)[] = [];
  return {
    getItem: (key: string) => new Promise<string | null>((resolve) => pending.push(() => resolve(items.get(key) ?? null))),
    setItem: (key: string, value: string) => Promise.resolve(void items.set(key, value)),
    release: () => pending.splice(0).forEach((fn) => fn()),
  };
}

// offline is a fetch with no network.
const offline: typeof fetch = () => Promise.reject(new TypeError('Failed to fetch'));

const betaTrue = { beta: { type: 'boolean' as const, value: true } };

// seed evaluates for context against realm once, leaving its values in storage.
const seed = (address: string, storage: StorageAdapter, context: EvaluationContext) =>
  seedStorage(address, storage, context, '/prod/web/');

describe('BrowserClient bootstrap', () => {
  it('serves bootstrap values until the evaluation arrives, without storing them', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    const client = new BrowserClient({
      address: realm.address,
      context: { key: 'u1' },
      bootstrap: betaTrue,
      storage,
      streaming: false,
    });
    cleanup.push(() => client.stop());
    expect(client.source()).toBe('bootstrap');
    expect(client.isReady()).toBe(false);
    expect(client.bool('beta', false)).toBe(true);
    expect(storage.items.size).toBe(0);

    const changed = vi.fn();
    client.subscribe(changed);
    await client.start();
    expect(client.source()).toBe('network');
    expect(client.isReady()).toBe(true);
    expect(client.bool('beta', true)).toBe(false);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(entries(storage.items)).toEqual(['{"flags":{"beta":{"type":"boolean","value":false}}}']);
  });

  it('reports the source turning network, and stores, when the evaluation matches bootstrap', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    const client = new BrowserClient({
      address: realm.address,
      context: { key: 'vip' },
      bootstrap: betaTrue,
      storage,
      streaming: false,
    });
    cleanup.push(() => client.stop());
    const changed = vi.fn();
    client.subscribe(changed);
    await client.start();
    expect(client.source()).toBe('network');
    expect(changed).toHaveBeenCalledTimes(1);
    expect(entries(storage.items)).toEqual([JSON.stringify({ flags: betaTrue })]);
  });

  it('applies only to the context it was given for', async () => {
    const realm = await setup();
    realm.setStatus(503);
    const client = new BrowserClient({
      address: realm.address,
      context: { key: 'u1' },
      bootstrap: betaTrue,
      onError: () => {},
    });
    cleanup.push(() => client.stop());
    await client.identify({ key: 'u2' });
    expect(client.source()).toBeUndefined();
    expect(client.bool('beta', false)).toBe(false);

    await client.identify({ key: 'u1' });
    expect(client.source()).toBe('bootstrap');
    expect(client.bool('beta', false)).toBe(true);
  });
});

describe('BrowserClient storage', () => {
  it('starts offline from the values stored for the same context', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    await seed(realm.address, storage, { key: 'vip' });

    // a reload with no network
    const client = new BrowserClient({ address: realm.address, path: '/prod/web/', context: { key: 'vip' }, storage, fetch: offline, onError: () => {} });
    cleanup.push(() => client.stop());
    expect(client.source()).toBe('cache');
    expect(client.isReady()).toBe(false);
    expect(client.bool('beta', false)).toBe(true);

    await expect(client.start()).rejects.toThrow('Failed to fetch');
    // a failed start keeps the stored values
    expect(client.source()).toBe('cache');
    expect(client.bool('beta', false)).toBe(true);
  });

  it('never serves values stored for another context', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    await seed(realm.address, storage, { key: 'vip' });

    // another user on the same device
    const other = new BrowserClient({ address: realm.address, path: '/prod/web/', context: { key: 'u1' }, storage, fetch: offline, onError: () => {} });
    expect(other.source()).toBeUndefined();
    expect(other.allFlags()).toEqual({});
    expect(other.bool('beta', false)).toBe(false);

    // and switching users on one client
    const client = new BrowserClient({ address: realm.address, path: '/prod/web/', context: { key: 'vip' }, storage, fetch: offline, onError: () => {} });
    expect(client.bool('beta', false)).toBe(true);
    await client.identify({ key: 'u1' });
    expect(client.source()).toBeUndefined();
    expect(client.bool('beta', false)).toBe(false);
    await client.identify({ key: 'vip' });
    expect(client.source()).toBe('cache');
    expect(client.bool('beta', false)).toBe(true);
  });

  it('serves stored values on identify while the evaluation is pending, then the evaluation', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    await seed(realm.address, storage, { key: 'vip' });
    const client = new BrowserClient({ address: realm.address, path: '/prod/web/', context: { key: 'u1' }, storage, onError: () => {} });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.bool('beta', true)).toBe(false);

    realm.setHang(true);
    const pending = client.identify({ key: 'vip' });
    expect(client.source()).toBe('cache');
    expect(client.isReady()).toBe(false);
    expect(client.bool('beta', false)).toBe(true);
    realm.setHang(false);
    client.stop();
    await pending.catch(() => {});
  });

  it('wins over bootstrap values for the same context', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    await seed(realm.address, storage, { key: 'u1' });
    const client = new BrowserClient({
      address: realm.address,
      path: '/prod/web/',
      context: { key: 'u1' },
      bootstrap: betaTrue,
      storage,
      fetch: offline,
    });
    expect(client.source()).toBe('cache');
    expect(client.bool('beta', true)).toBe(false);
  });

  it('is written on every change', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    const client = new BrowserClient({ address: realm.address, context: { key: 'u1' }, storage });
    cleanup.push(() => client.stop());
    await client.start();
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    const key = storageKey(new URL('/v1/evaluate/', realm.address).toString(), { key: 'u1' });
    expect(decodeFlags(storage.items.get(key))).toEqual({ beta: { type: 'boolean', value: false } });

    realm.publish({ rules: { beta: { type: 'boolean', value: true }, limit: { type: 'number', value: 3 } } });
    await vi.waitFor(() =>
      expect(decodeFlags(storage.items.get(key))).toEqual({
        beta: { type: 'boolean', value: true },
        limit: { type: 'number', value: 3 },
      }),
    );
  });

  it('is keyed by server, chamber, and a hash of the context', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    await seed(realm.address, storage, { key: 'vip', attributes: { tenantId: 't-secret', location: 'l1' } });
    const [key] = [...storage.items.keys()].filter((k) => k !== recentKey(`${realm.address}/v1/evaluate/prod/web/`));
    expect(key).toMatch(new RegExp(`^realm:v1:${realm.address}/v1/evaluate/prod/web/:[0-9a-f]{40}$`));
    expect(key).not.toContain('vip');
    expect(key).not.toContain('t-secret');

    const offlineClient = (path: string, context: object) =>
      new BrowserClient({ address: realm.address, path, context, storage, fetch: offline });
    // the same context with its attributes in another order
    expect(offlineClient('/prod/web/', { key: 'vip', attributes: { location: 'l1', tenantId: 't-secret' } }).source()).toBe('cache');
    expect(offlineClient('/prod/web/', { key: 'vip', attributes: { tenantId: 't-secret' } }).source()).toBeUndefined();
    expect(offlineClient('/prod/api/', { key: 'vip', attributes: { tenantId: 't-secret', location: 'l1' } }).source()).toBeUndefined();
  });

  it('ignores entries it did not write', async () => {
    const storage = memoryStorage();
    const key = storageKey('http://127.0.0.1:1/v1/evaluate/', { key: 'u1' });
    const client = () => new BrowserClient({ address: 'http://127.0.0.1:1', context: { key: 'u1' }, storage, fetch: offline });
    for (const raw of ['not json', 'null', '{"flags":[]}', '{"flags":{"beta":{"type":"date","value":1}}}', '{"flags":{"beta":{"type":"boolean"}}}']) {
      storage.items.set(key, raw);
      expect(client().source(), raw).toBeUndefined();
    }
  });

  it('reports storage failures and carries on', async () => {
    const realm = await setup();
    const onError = vi.fn();
    const storage: StorageAdapter = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('quota exceeded');
      },
    };
    const client = new BrowserClient({ address: realm.address, context: { key: 'vip' }, storage, onError, streaming: false });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.bool('beta', false)).toBe(true);
    expect(onError.mock.calls.map(([e]) => (e as Error).message)).toEqual([
      'flag storage failed: blocked', // reading the context's values
      'flag storage failed: blocked', // reading the list of recent contexts
      'flag storage failed: quota exceeded',
    ]);
  });

  it('does not write values storage already holds', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    await seed(realm.address, storage, { key: 'vip' });
    const setItem = vi.spyOn(storage, 'setItem');
    const client = new BrowserClient({ address: realm.address, path: '/prod/web/', context: { key: 'vip' }, storage, streaming: false });
    cleanup.push(() => client.stop());
    await client.start();
    expect(client.source()).toBe('network');
    expect(setItem).not.toHaveBeenCalled();
  });

  it('keeps the values of only the most recently used contexts', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    const stored = (key: string) => storage.items.has(storageKey(`${realm.address}/v1/evaluate/prod/web/`, { key }));
    for (let i = 0; i < maxStoredContexts + 2; i++) await seed(realm.address, storage, { key: `u${i}` });
    expect(entries(storage.items)).toHaveLength(maxStoredContexts);
    expect([stored('u0'), stored('u1'), stored('u2'), stored('u11')]).toEqual([false, false, true, true]);

    // an evaluation for u2, unchanged, makes it the most recently used
    await seed(realm.address, storage, { key: 'u2' });
    await seed(realm.address, storage, { key: 'u12' });
    expect([stored('u2'), stored('u3'), stored('u12')]).toEqual([true, false, true]);
    expect(entries(storage.items)).toHaveLength(maxStoredContexts);
  });

  it('empties the values it no longer keeps where storage cannot remove them', async () => {
    const realm = await setup();
    const { items, getItem, setItem } = memoryStorage();
    for (let i = 0; i <= maxStoredContexts; i++) await seed(realm.address, { getItem, setItem }, { key: `u${i}` });
    expect(items.get(storageKey(`${realm.address}/v1/evaluate/prod/web/`, { key: 'u0' }))).toBe('');
    expect(entries(items).filter((raw) => raw !== '')).toHaveLength(maxStoredContexts);
  });

  it('removes nothing but its own values for a list of recent contexts it did not write', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    const list = recentKey(`${realm.address}/v1/evaluate/prod/web/`);
    const other = list.replace(/recent$/, 'other');
    storage.items.set(other, 'kept');
    const full = Array.from({ length: maxStoredContexts }, (_, i) => i.toString(16).repeat(40));
    for (const raw of ['not json', '{"recent":[]}', JSON.stringify([...full, 'other', 7])]) {
      storage.items.set(list, raw);
      await seed(realm.address, storage, { key: 'u1' });
      expect(storage.items.get(other), raw).toBe('kept');
      expect(JSON.parse(storage.items.get(list)!)[0]).toMatch(/^[0-9a-f]{40}$/);
    }
  });

  it('never stores a response that is not an evaluation', async () => {
    const realm = await setup();
    realm.intercept((_req, res) => {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end('{"data":null}');
      return true;
    });
    const storage = memoryStorage();
    const client = new BrowserClient({
      address: realm.address,
      context: { key: 'u1' },
      bootstrap: betaTrue,
      storage,
      streaming: false,
    });
    cleanup.push(() => client.stop());
    await expect(client.start()).rejects.toThrow('the server did not return an evaluation');
    expect(client.source()).toBe('bootstrap');
    expect(client.bool('beta', false)).toBe(true);
    expect(storage.items.size).toBe(0);
  });

  it('shares values between contexts the server evaluates the same', async () => {
    const realm = await setup();
    const storage = memoryStorage();
    // as a JavaScript caller might pass it: the server receives "7"
    await seed(realm.address, storage, { key: 'vip', attributes: { build: 7 } } as unknown as EvaluationContext);
    const client = new BrowserClient({
      address: realm.address,
      path: '/prod/web/',
      context: { key: 'vip', attributes: { build: '7' } },
      storage,
      fetch: offline,
    });
    expect(client.source()).toBe('cache');
  });

  it('applies values read asynchronously, unless the context changed or the server answered first', async () => {
    const realm = await setup();
    const seeded = memoryStorage();
    await seed(realm.address, seeded, { key: 'vip' });
    const items = seeded.items;
    const opts = { address: realm.address, path: '/prod/web/', fetch: offline, onError: () => {} };

    const storage = asyncStorage(items);
    const client = new BrowserClient({ ...opts, context: { key: 'vip' }, bootstrap: {}, storage });
    expect(client.source()).toBe('bootstrap');
    const changed = vi.fn();
    client.subscribe(changed);
    storage.release();
    await vi.waitFor(() => expect(client.source()).toBe('cache'));
    expect(client.bool('beta', false)).toBe(true);
    expect(changed).toHaveBeenCalledTimes(1);

    // the read finishes after identify switched to another user
    const switching = asyncStorage(items);
    const other = new BrowserClient({ ...opts, context: { key: 'vip' }, storage: switching });
    await other.identify({ key: 'u1' });
    switching.release();
    await new Promise((r) => setTimeout(r, 10));
    expect(other.source()).toBeUndefined();
    expect(other.bool('beta', false)).toBe(false);

    // the read finishes after the server answered
    items.set(storageKey(`${realm.address}/v1/evaluate/prod/web/`, { key: 'u1' }), JSON.stringify({ flags: betaTrue }));
    const slow = asyncStorage(items);
    const online = new BrowserClient({ address: realm.address, path: '/prod/web/', context: { key: 'u1' }, storage: slow, streaming: false });
    cleanup.push(() => online.stop());
    await online.start();
    expect(online.bool('beta', true)).toBe(false);
    slow.release();
    await new Promise((r) => setTimeout(r, 10));
    expect(online.source()).toBe('network');
    expect(online.bool('beta', true)).toBe(false);
  });
});

describe('localStorageAdapter', () => {
  it('reads and writes a Web Storage area', () => {
    const adapter = localStorageAdapter(memoryStorage() as unknown as Storage);
    adapter.setItem('a', '1');
    expect(adapter.getItem('a')).toBe('1');
    expect(adapter.getItem('b')).toBeNull();
  });

  it('stores nothing where there is no localStorage', () => {
    vi.stubGlobal('localStorage', undefined);
    cleanup.push(() => vi.unstubAllGlobals());
    const adapter = localStorageAdapter();
    expect(() => adapter.setItem('a', '1')).not.toThrow();
    expect(adapter.getItem('a')).toBeUndefined();
  });

  it('stores nothing where reading localStorage throws', () => {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get() {
        throw new DOMException('denied', 'SecurityError');
      },
    });
    cleanup.push(() => {
      if (descriptor) Object.defineProperty(globalThis, 'localStorage', descriptor);
      else delete (globalThis as { localStorage?: unknown }).localStorage;
    });
    const adapter = localStorageAdapter();
    expect(() => adapter.setItem('a', '1')).not.toThrow();
    expect(adapter.getItem('a')).toBeUndefined();
  });
});
