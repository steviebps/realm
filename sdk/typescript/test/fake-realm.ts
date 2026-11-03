import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';

// fakeRealm serves one chamber the way the realm server does, and lets tests
// push changes to open event streams.
export async function fakeRealm() {
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
