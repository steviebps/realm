import { readFileSync } from 'node:fs';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { RealmError, ServerClient, type EvaluationContext } from '../src/index.js';
import { parseTemplate } from '../src/template.js';

// The cases the Go SDK runs too (see docs/design/subtree.md).
const conformance = JSON.parse(readFileSync(new URL('../../conformance/subtree.json', import.meta.url), 'utf8')) as {
  cases: {
    name: string;
    template: string;
    root: string;
    paths: string[];
    key?: string;
    attributes?: Record<string, string>;
    defaults?: Record<string, string>;
    expect: string | null;
  }[];
  invalidTemplates: string[];
};

let cleanup: (() => unknown)[] = [];
afterEach(async () => {
  for (const fn of cleanup.reverse()) await fn();
  cleanup = [];
});

// shortestWaits makes every backoff and jitter take its shortest value, so
// tests that wait for reconnects are quick.
function shortestWaits() {
  const random = vi.spyOn(Math, 'random').mockReturnValue(0);
  cleanup.push(() => random.mockRestore());
}

/** A subtree entry, as a read or a chamber event carries it. */
interface Entry {
  path: string;
  rules?: Record<string, unknown>;
  error?: string;
  revision?: number;
}

// view returns the entry of a view at path whose flag "where" is path, and
// whose flag "beta" is beta.
function view(path: string, revision?: number, beta = false): Entry {
  return {
    path,
    rules: { where: { type: 'string', value: path }, beta: { type: 'boolean', value: beta } },
    ...(revision === undefined ? {} : { revision }),
  };
}

/** An open event stream of the fake server. */
interface Stream {
  send(event: string, data: unknown, id?: number): void;
  /** Sends a chamber event for entry, with its revision as the event's id. */
  chamber(entry: Entry): void;
  /** Sends a snapshot: a chamber event for each entry, then synced. */
  snapshot(entries: Entry[]): void;
  /** Ends the stream, as a server does at a stream's maximum lifetime. */
  end(): void;
}

// fakeRealm returns a fetch that answers subtree reads with read() (an
// envelope's data, or a Response) and opens an event stream for each watch,
// which the test writes to.
function fakeRealm(read: () => unknown = () => ({ chambers: [] })) {
  const requests: string[] = [];
  const streams: Stream[] = [];
  const encoder = new TextEncoder();
  const state = { read };
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const url = new URL(String(input));
    requests.push(url.pathname + url.search);
    if (url.searchParams.get('watch') !== 'true') {
      const answer = state.read();
      return answer instanceof Response ? answer : Response.json({ data: answer });
    }
    let controller!: ReadableStreamDefaultController<Uint8Array>;
    let open = true;
    const body = new ReadableStream<Uint8Array>({
      start: (c) => {
        controller = c;
      },
      cancel: () => {
        open = false;
      },
    });
    const write = (text: string) => {
      if (open) controller.enqueue(encoder.encode(text));
    };
    init?.signal?.addEventListener('abort', () => {
      if (open) controller.error(init.signal!.reason);
      open = false;
    });
    const stream: Stream = {
      send: (event, data, id) =>
        write(`event: ${event}\n${id === undefined ? '' : `id: ${id}\n`}data: ${typeof data === 'string' ? data : JSON.stringify(data)}\n\n`),
      chamber({ revision, ...entry }) {
        this.send('chamber', entry, revision);
      },
      snapshot(entries) {
        for (const entry of entries) this.chamber(entry);
        this.send('synced', { chambers: entries.length });
      },
      end() {
        if (open) controller.close();
        open = false;
      },
    };
    streams.push(stream);
    return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } });
  };
  return {
    fetch,
    requests,
    streams,
    /** The newest stream. */
    stream: () => streams.at(-1)!,
    setRead: (next: () => unknown) => (state.read = next),
  };
}

const template = '/prod/tenants/{tenantId}/{locationId}/';
const ctx = (tenantId?: string, locationId?: string): EvaluationContext => ({
  attributes: { ...(tenantId ? { tenantId } : {}), ...(locationId ? { locationId } : {}) },
});

function client(fetch: typeof globalThis.fetch, options: { streaming?: boolean; onError?: (e: Error) => void } = {}) {
  const c = new ServerClient({
    address: 'http://realm.test',
    pathTemplate: template,
    streaming: options.streaming ?? false,
    pollingInterval: 20,
    onError: options.onError ?? (() => {}),
    fetch,
  });
  cleanup.push(() => c.stop());
  return c;
}

describe('subtree conformance', () => {
  describe.each(conformance.cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    it('resolves', async () => {
      const realm = fakeRealm(() => ({ chambers: c.paths.map((p) => view(p)) }));
      const client = new ServerClient({
        address: 'http://realm.test',
        pathTemplate: c.template,
        attributes: c.defaults,
        streaming: false,
        fetch: realm.fetch,
      });
      cleanup.push(() => client.stop());
      expect(parseTemplate(c.template).root).toBe(c.root);
      await client.start();
      expect(realm.requests[0]).toBe(`/v1/chambers${c.root}?subtree=true`);

      const context: EvaluationContext = { key: c.key, attributes: c.attributes };
      expect(client.chamberPath(context)).toBe(c.expect ?? undefined);
      expect(client.string('where', context, 'none')).toBe(c.expect ?? 'none');
    });
  });

  it.each(conformance.invalidTemplates)('refuses the template %j', (t) => {
    expect(() => new ServerClient({ address: 'http://realm.test', pathTemplate: t })).toThrow(TypeError);
  });

  it('refuses a template with a path', () => {
    expect(() => new ServerClient({ address: 'http://realm.test', path: '/prod/', pathTemplate: template })).toThrow(
      'the path and pathTemplate options cannot be combined',
    );
  });

  it('refuses "." and ".." segments', () => {
    expect(() => parseTemplate('/prod/../{tenantId}/')).toThrow(TypeError);
    expect(() => parseTemplate('/prod/./{tenantId}/')).toThrow(TypeError);
    expect(parseTemplate('/prod/v1.2/{tenant.id}/')).toEqual({ root: '/prod/v1.2/', segments: [{ attribute: 'tenant.id' }] });
  });
});

describe('ServerClient with a path template', () => {
  it('starts empty', () => {
    const c = client(fakeRealm().fetch);
    expect(c.isReady()).toBe(false);
    expect(c.chamberPath(ctx('t-1'))).toBeUndefined();
    expect(c.revision(ctx('t-1'))).toBeUndefined();
    expect(c.bool('beta', ctx('t-1'), true)).toBe(true);
    expect(c.allFlags(ctx('t-1'))).toEqual({});
  });

  it('polls the subtree and evaluates each context against its chamber', async () => {
    let chambers = [view('/prod/tenants/', 3), view('/prod/tenants/t-1/', 5, true), view('/prod/tenants/t-1/loc-1/', 7)];
    const realm = fakeRealm(() => ({ chambers }));
    const c = client(realm.fetch);
    const changed = vi.fn();
    c.subscribe(changed);
    await c.start();

    expect(c.isReady()).toBe(true);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(c.bool('beta', ctx('t-1'), false)).toBe(true);
    expect(c.bool('beta', ctx('t-1', 'loc-1'), true)).toBe(false);
    expect(c.bool('beta', ctx('t-2'), true)).toBe(false);
    expect(c.revision(ctx('t-1', 'loc-1'))).toBe(7);
    expect(c.revision(ctx('t-1', 'loc-2'))).toBe(5);
    expect(c.revision()).toBe(3);
    expect(c.chamberPath()).toBe('/prod/tenants/');
    expect(c.allFlags(ctx('t-1', 'loc-1'))).toEqual({
      where: { type: 'string', value: '/prod/tenants/t-1/loc-1/' },
      beta: { type: 'boolean', value: false },
    });

    // unchanged polls notify no one
    const polls = realm.requests.length;
    await vi.waitFor(() => expect(realm.requests.length).toBeGreaterThan(polls + 1));
    expect(changed).toHaveBeenCalledTimes(1);

    // a poll that drops a chamber and changes another notifies once
    chambers = [view('/prod/tenants/', 3), view('/prod/tenants/t-1/', 6, false)];
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-1', 'loc-1'))).toBe('/prod/tenants/t-1/'));
    expect(c.bool('beta', ctx('t-1'), true)).toBe(false);
    expect(c.revision(ctx('t-1'))).toBe(6);
    expect(changed).toHaveBeenCalledTimes(2);
    expect(realm.requests.every((r) => r === '/v1/chambers/prod/tenants/?subtree=true')).toBe(true);
  });

  it('keeps the view held for a path the server could not build', async () => {
    let chambers: Entry[] = [view('/prod/tenants/'), { path: '/prod/tenants/t-1/', error: 'could not parse' }];
    const realm = fakeRealm(() => ({ chambers }));
    const c = client(realm.fetch);
    await c.start();
    // never built: its context falls back to the root
    expect(c.chamberPath(ctx('t-1'))).toBe('/prod/tenants/');

    chambers = [view('/prod/tenants/'), view('/prod/tenants/t-1/', 2, true)];
    await vi.waitFor(() => expect(c.bool('beta', ctx('t-1'), false)).toBe(true));
    chambers = [view('/prod/tenants/'), { path: '/prod/tenants/t-1/', error: 'could not parse' }];
    const polls = realm.requests.length;
    await vi.waitFor(() => expect(realm.requests.length).toBeGreaterThan(polls + 1));
    expect(c.bool('beta', ctx('t-1'), false)).toBe(true);
    expect(c.revision(ctx('t-1'))).toBe(2);
  });

  it.each([
    ['a chamber', { rules: {} }],
    ['an entry without a path', { chambers: [{ rules: {} }] }],
    ['an entry with neither rules nor an error', { chambers: [{ path: '/prod/tenants/' }] }],
    ['a path that is not canonical', { chambers: [{ path: 'prod/tenants', rules: {} }] }],
  ])('refuses %s, keeping the last subtree', async (_name, answer) => {
    const realm = fakeRealm(() => answer);
    const onError = vi.fn();
    const c = client(realm.fetch, { onError });
    const err = await c.start().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(RealmError);
    expect(c.isReady()).toBe(false);

    realm.setRead(() => ({ chambers: [view('/prod/tenants/')] }));
    await vi.waitFor(() => expect(c.isReady()).toBe(true), { timeout: 3000 });
    const reported = onError.mock.calls.length;
    realm.setRead(() => answer);
    await vi.waitFor(() => expect(onError.mock.calls.length).toBeGreaterThan(reported), { timeout: 3000 });
    expect(onError.mock.calls.at(-1)?.[0]).toBeInstanceOf(RealmError);
    expect(c.isReady()).toBe(true);
    expect(c.chamberPath(ctx('t-1'))).toBe('/prod/tenants/');
  });

  it('refuses a body that ends early', async () => {
    const realm = fakeRealm(
      () => new Response('{"data":{"chambers":[{"path":"/prod/tenants/","rules":{}}', { headers: { 'Content-Type': 'application/json' } }),
    );
    const c = client(realm.fetch);
    await expect(c.start()).rejects.toThrow('the server did not return JSON');
    expect(c.isReady()).toBe(false);
  });

  it('watches the subtree: a snapshot up to synced, then each change', async () => {
    shortestWaits();
    const realm = fakeRealm(() => ({ chambers: [view('/prod/tenants/', 1)] }));
    const onError = vi.fn();
    const c = client(realm.fetch, { streaming: true, onError });
    const changed = vi.fn();
    c.subscribe(changed);
    await c.start();
    expect(changed).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(realm.streams).toHaveLength(1));
    expect(realm.requests).toEqual(['/v1/chambers/prod/tenants/?subtree=true', '/v1/chambers/prod/tenants/?subtree=true&watch=true']);

    // nothing applies before synced
    const s = realm.stream();
    s.send('chamber', { path: '/prod/tenants/', rules: view('/').rules }, 2);
    s.send('chamber', { path: '/prod/tenants/t-1/', rules: view('/prod/tenants/t-1/').rules }, 3);
    s.send('surprise', { from: 'a newer server' });
    await new Promise((r) => setTimeout(r, 20));
    expect(c.chamberPath(ctx('t-1'))).toBe('/prod/tenants/');
    expect(c.revision()).toBe(1);
    s.send('synced', { chambers: 2 });
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-1'))).toBe('/prod/tenants/t-1/'));
    expect(c.revision()).toBe(2);
    expect(c.revision(ctx('t-1'))).toBe(3);
    expect(changed).toHaveBeenCalledTimes(2);

    // then each change applies as it arrives
    s.send('chamber', { path: '/prod/tenants/t-1/loc-1/', rules: view('/x/', undefined, true).rules }, 4);
    await vi.waitFor(() => expect(c.bool('beta', ctx('t-1', 'loc-1'), false)).toBe(true));
    expect(c.revision(ctx('t-1', 'loc-1'))).toBe(4);
    expect(changed).toHaveBeenCalledTimes(3);
    s.send('deleted', { path: '/prod/tenants/t-1/' });
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-1', 'loc-2'))).toBe('/prod/tenants/'));
    expect(c.chamberPath(ctx('t-1', 'loc-1'))).toBe('/prod/tenants/t-1/loc-1/');
    expect(changed).toHaveBeenCalledTimes(4);

    // an unchanged view takes the new revision without notifying
    s.send('chamber', { path: '/prod/tenants/t-1/loc-1/', rules: view('/x/', undefined, true).rules }, 9);
    await vi.waitFor(() => expect(c.revision(ctx('t-1', 'loc-1'))).toBe(9));
    expect(changed).toHaveBeenCalledTimes(4);
    expect(onError).not.toHaveBeenCalled();
    expect(realm.streams).toHaveLength(1);
  });

  it('fills from the stream after a failed first load', async () => {
    shortestWaits();
    const realm = fakeRealm(() => new Response('{"errors":["unavailable"]}', { status: 503 }));
    const c = client(realm.fetch, { streaming: true });
    await expect(c.start()).rejects.toMatchObject({ status: 503 });
    await vi.waitFor(() => expect(realm.streams).toHaveLength(1), { timeout: 3000 });
    realm.stream().snapshot([view('/prod/tenants/'), view('/prod/tenants/t-1/', 5)]);
    await vi.waitFor(() => expect(c.isReady()).toBe(true));
    expect(c.revision(ctx('t-1'))).toBe(5);
    await expect(c.start()).resolves.toBeUndefined();
  });

  it('drops the paths a reconnected stream did not send, after its synced', async () => {
    shortestWaits();
    const realm = fakeRealm(() => new Response('{"errors":["unavailable"]}', { status: 503 }));
    const c = client(realm.fetch, { streaming: true });
    await c.start().catch(() => {});
    await vi.waitFor(() => expect(realm.streams).toHaveLength(1), { timeout: 3000 });
    realm.stream().snapshot([view('/prod/tenants/'), view('/prod/tenants/t-1/'), view('/prod/tenants/t-2/')]);
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-2'))).toBe('/prod/tenants/t-2/'));

    realm.stream().end();
    await vi.waitFor(() => expect(realm.streams).toHaveLength(2), { timeout: 3000 });
    const changed = vi.fn();
    c.subscribe(changed);
    const s = realm.stream();
    s.chamber(view('/prod/tenants/'));
    s.chamber(view('/prod/tenants/t-1/', 8));
    await new Promise((r) => setTimeout(r, 20));
    expect(c.chamberPath(ctx('t-2'))).toBe('/prod/tenants/t-2/');
    s.send('synced', { chambers: 2 });
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-2'))).toBe('/prod/tenants/'));
    expect(c.revision(ctx('t-1'))).toBe(8);
    expect(changed).toHaveBeenCalledTimes(1);
  });

  it('drops the paths a resync did not send', async () => {
    shortestWaits();
    const realm = fakeRealm(() => ({ chambers: [view('/prod/tenants/')] }));
    const c = client(realm.fetch, { streaming: true });
    await c.start();
    await vi.waitFor(() => expect(realm.streams).toHaveLength(1));
    const s = realm.stream();
    s.snapshot([view('/prod/tenants/'), view('/prod/tenants/t-1/'), view('/prod/tenants/t-2/')]);
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-2'))).toBe('/prod/tenants/t-2/'));

    s.snapshot([view('/prod/tenants/'), view('/prod/tenants/t-1/', 4)]);
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-2'))).toBe('/prod/tenants/'));
    expect(c.revision(ctx('t-1'))).toBe(4);
    expect(realm.streams).toHaveLength(1);
  });

  it('keeps the view held for an error entry in the stream', async () => {
    shortestWaits();
    const realm = fakeRealm(() => ({ chambers: [view('/prod/tenants/'), view('/prod/tenants/t-1/', 2, true)] }));
    const c = client(realm.fetch, { streaming: true });
    await c.start();
    await vi.waitFor(() => expect(realm.streams).toHaveLength(1));
    realm.stream().snapshot([view('/prod/tenants/'), { path: '/prod/tenants/t-1/', error: 'could not parse' }]);
    // let synced apply
    realm.stream().chamber(view('/prod/tenants/t-9/'));
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-9'))).toBe('/prod/tenants/t-9/'));
    expect(c.bool('beta', ctx('t-1'), false)).toBe(true);
    expect(c.revision(ctx('t-1'))).toBe(2);
  });

  it('reports events that are not subtree events, keeping the views', async () => {
    shortestWaits();
    const realm = fakeRealm(() => ({ chambers: [view('/prod/tenants/'), view('/prod/tenants/t-1/', 2, true)] }));
    const onError = vi.fn();
    const c = client(realm.fetch, { streaming: true, onError });
    await c.start();
    await vi.waitFor(() => expect(realm.streams).toHaveLength(1));
    const s = realm.stream();
    s.snapshot([view('/prod/tenants/'), view('/prod/tenants/t-1/', 2, true)]);

    // a single-chamber event, from a server that ignores subtree=true
    s.send('chamber', { rules: {} });
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));
    expect(onError.mock.calls[0]?.[0]).toBeInstanceOf(RealmError);
    expect(onError.mock.calls[0]?.[0].message).toMatch(/without a chamber path/);
    s.send('chamber', 'not json');
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(2));
    expect(onError.mock.calls[1]?.[0].message).toBe('the server sent an event that is not JSON');
    s.send('chamber', { path: '/prod/tenants/t-1/', rules: [] });
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(3));
    expect(c.bool('beta', ctx('t-1'), false)).toBe(true);

    // a resync after an event without a path drops nothing it cannot be sure of
    s.snapshot([view('/prod/tenants/')]);
    s.chamber(view('/prod/tenants/t-9/'));
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-9'))).toBe('/prod/tenants/t-9/'));
    expect(c.chamberPath(ctx('t-1'))).toBe('/prod/tenants/t-1/');
    // the next one does
    s.snapshot([view('/prod/tenants/')]);
    await vi.waitFor(() => expect(c.chamberPath(ctx('t-1'))).toBe('/prod/tenants/'));
    expect(realm.streams).toHaveLength(1);
  });
});

describe('ServerClient with a path', () => {
  it('reads one chamber, and reports its path once it has arrived', async () => {
    const realm = fakeRealm(() => ({ rules: { beta: { type: 'boolean', value: true } } }));
    const c = new ServerClient({ address: 'http://realm.test', path: 'prod/web', streaming: false, fetch: realm.fetch });
    cleanup.push(() => c.stop());
    expect(c.chamberPath()).toBeUndefined();
    await c.start();
    expect(realm.requests).toEqual(['/v1/chambers/prod/web/']);
    expect(c.chamberPath(ctx('t-1'))).toBe('/prod/web/');
    expect(c.bool('beta', ctx('t-1'), false)).toBe(true);
  });
});
