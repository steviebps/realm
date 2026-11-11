import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { BrowserClient, type EvaluationContext, type StorageAdapter } from '../src/index.js';

/** A request the fake server received, and when (Date.now()). */
export interface FakeRequest {
  method: string;
  url: string;
  auth: string | undefined;
  body: string;
  at: number;
}

// fakeRealm serves one chamber the way the realm server does, and lets tests
// push changes to open event streams.
export async function fakeRealm() {
  let chamber: unknown = { rules: { beta: { type: 'boolean', value: false } } };
  const streams = new Set<{ res: ServerResponse; send: () => void }>();
  const requests: FakeRequest[] = [];
  let status = 200;
  let hang = false;
  // The revision reported with each read and event, like a server on
  // transactional storage; none by default.
  let revision: string | undefined;
  // A test's own answer to some requests, like a proxy in front of the
  // server: it returns true when it has answered.
  let intercept: ((req: FakeRequest, res: ServerResponse) => boolean) | undefined;

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
    const request = { method: req.method!, url: req.url!, auth: req.headers.authorization, body, at: Date.now() };
    requests.push(request);
    if (hang) return; // never respond, like a server behind a dead network
    if (intercept?.(request, res)) return;
    if (status !== 200) {
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ errors: ['not allowed'] }));
      return;
    }
    const isEvaluate = req.url!.startsWith('/v1/evaluate/');
    const payload = () => JSON.stringify(isEvaluate ? evaluate(body) : chamber);
    if (req.url!.endsWith('?watch=true')) {
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      const stream = {
        res,
        send: () =>
          res.write(`event: ${isEvaluate ? 'flags' : 'chamber'}\n${revision ? `id: ${revision}\n` : ''}data: ${payload()}\n\n`),
      };
      streams.add(stream);
      res.on('close', () => streams.delete(stream));
      stream.send();
      return;
    }
    res.writeHead(200, { 'Content-Type': 'application/json', ...(revision ? { 'X-Realm-Revision': revision } : {}) });
    res.end(JSON.stringify({ data: JSON.parse(payload()) }));
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;

  return {
    address,
    requests,
    streams,
    setStatus: (s: number) => (status = s),
    setHang: (h: boolean) => (hang = h),
    intercept: (fn: ((req: FakeRequest, res: ServerResponse) => boolean) | undefined) => (intercept = fn),
    /** Writes text, such as a malformed event, to every open stream. */
    write(text: string) {
      for (const s of streams) s.res.write(text);
    },
    /** Sets the revision reported from now on, or none. */
    setRevision: (r: string | undefined) => (revision = r),
    /** Replaces the chamber, at revision rev when given, and sends it to every open stream. */
    publish(next: unknown, rev?: string) {
      chamber = next;
      if (rev !== undefined) revision = rev;
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

// seedStorage evaluates for context against the server at address once,
// leaving its values in storage.
export async function seedStorage(address: string, storage: StorageAdapter, context: EvaluationContext, path?: string) {
  const client = new BrowserClient({ address, path, context, storage, streaming: false });
  try {
    await client.start();
  } finally {
    client.stop();
  }
}
