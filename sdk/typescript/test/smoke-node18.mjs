// A dependency-free smoke test of the built package (dist/), for runtimes the
// test runner no longer supports. The package's engines field promises Node
// 18; CI builds on a current Node, then runs this with Node 18:
//
//   npm run build && node test/smoke-node18.mjs
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { BrowserClient, ServerClient, bucket, evaluateChamber, serverVisitorId } from '../dist/index.js';

const chamber = {
  rules: {
    beta: {
      type: 'boolean',
      value: false,
      targeting: [{ value: true, clauses: [{ attribute: 'key', op: 'in', values: ['vip'] }] }],
    },
  },
};

// local evaluation and bucketing
assert.equal(evaluateChamber(chamber, { key: 'vip' }).beta.value, true);
assert.equal(evaluateChamber(chamber, { key: 'u1' }).beta.value, false);
const b = bucket('seed', 'key');
assert.ok(b >= 0 && b < 100); // a percentage
assert.equal(bucket('seed', 'key'), b);

// visitor IDs (Node 18's global crypto)
const visitor = serverVisitorId(undefined);
assert.match(visitor.id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
assert.deepEqual(serverVisitorId(visitor.setCookie.split(';')[0]), { id: visitor.id });

// both clients against a server that answers like realm (no streaming)
const server = createServer((req, res) => {
  let body = '';
  req.on('data', (c) => (body += c));
  req.on('end', () => {
    res.setHeader('Content-Type', 'application/json');
    if (req.url.startsWith('/v1/evaluate/')) {
      const key = body ? JSON.parse(body).key : undefined;
      res.end(JSON.stringify({ data: { flags: { beta: { type: 'boolean', value: key === 'vip' } } } }));
    } else {
      res.end(JSON.stringify({ data: chamber }));
    }
  });
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const address = `http://127.0.0.1:${server.address().port}`;

try {
  const local = new ServerClient({ address, streaming: false });
  await local.start();
  assert.equal(local.bool('beta', { key: 'vip' }, false), true);
  local.stop();

  const remote = new BrowserClient({ address, context: { key: 'vip' }, streaming: false });
  await remote.start();
  assert.equal(remote.isReady(), true);
  assert.equal(remote.bool('beta', false), true);
  await remote.identify({ key: 'u1' });
  assert.equal(remote.bool('beta', true), false);
  remote.stop();
} finally {
  server.close();
}
console.log(`smoke test passed on Node ${process.versions.node}`);
