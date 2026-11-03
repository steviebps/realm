import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BrowserClient, RealmError, ServerClient } from '../src/index.js';

// fakeRealm serves one chamber the way the realm server does, and lets tests
// push changes to open event streams.
async function fakeRealm() {
  let chamber: unknown = { rules: { beta: { type: 'boolean', value: false } } };
  const streams = new Set<{ res: ServerResponse; send: () => void }>();
  const requests: { method: string; url: string; auth: string | undefined; body: string }[] = [];
  let status = 200;

  const evaluate = (body: string) => {
    const ctx = body ? (JSON.parse(body) as { key?: string }) : {};
    // a stand-in for server-side evaluation: beta is on for user "vip"
    const c = chamber as { rules: Record<string, { type: string; value: unknown }> };
    return {
      flags: Object.fromEntries(
        Object.entries(c.rules).map(([k, f]) => [k, { type: f.type, value: ctx.key === 'vip' ? true : f.value }]),
      ),
    };
  };

  const server = createServer(async (req: IncomingMessage, res: ServerResponse) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    requests.push({ method: req.method!, url: req.url!, auth: req.headers.authorization, body });
    if (status !== 200) {
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ errors: ['not allowed'] }));
      return;
    }
    const isEvaluate = req.url!.startsWith('/v1/evaluate/');
    const payload = () => JSON.stringify(isEvaluate ? evaluate(body) : chamber);
    if (req.url!.endsWith('?watch=true')) {
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      const stream = { res, send: () => res.write(`event: ${isEvaluate ? 'flags' : 'chamber'}\ndata: ${payload()}\n\n`) };
      streams.add(stream);
      res.on('close', () => streams.delete(stream));
      stream.send();
      return;
    }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ data: JSON.parse(payload()) }));
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;

  return {
    address,
    requests,
    streams,
    setStatus: (s: number) => (status = s),
    publish(next: unknown) {
      chamber = next;
      for (const s of streams) s.send();
    },
    dropStreams() {
      for (const s of streams) s.res.destroy();
    },
    close: () => {
      server.closeAllConnections();
      return new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

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
