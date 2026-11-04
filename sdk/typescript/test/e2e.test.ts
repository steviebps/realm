import { describe, expect, it, vi } from 'vitest';
import { BrowserClient, ServerClient } from '../src/index.js';

// Runs against a real server at REALM_E2E_ADDRESS (see scripts/e2e.sh), which
// serves the /e2e/ chamber: beta is false by default and true for key "vip".
const address = process.env.REALM_E2E_ADDRESS;

async function put(path: string, chamber: unknown) {
  const res = await fetch(`${address}/v1/chambers/${path}`, { method: 'POST', body: JSON.stringify(chamber) });
  expect(res.status).toBe(201);
}

describe.skipIf(!address)('against a real server', () => {
  it('ServerClient evaluates locally and follows changes', async () => {
    await put('e2e/ts/server/', { rules: { msg: { type: 'string', value: 'one' } } });
    const client = new ServerClient({ address: address!, path: '/e2e/ts/server/' });
    try {
      await client.start();
      expect(client.bool('beta', { key: 'vip' }, false)).toBe(true);
      expect(client.bool('beta', { key: 'u1' }, true)).toBe(false);
      expect(client.string('msg', undefined, '')).toBe('one');

      await put('e2e/ts/server/', { rules: { msg: { type: 'string', value: 'two' } } });
      await vi.waitFor(() => expect(client.string('msg', undefined, '')).toBe('two'), { timeout: 5000 });
    } finally {
      client.stop();
    }
  });

  it('BrowserClient evaluates on the server and re-evaluates on identify', async () => {
    const client = new BrowserClient({ address: address!, path: '/e2e/', context: { key: 'vip' } });
    try {
      await client.start();
      expect(client.isReady()).toBe(true);
      expect(client.bool('beta', false)).toBe(true);

      await client.identify({ key: 'u1' });
      expect(client.isReady()).toBe(true);
      expect(client.bool('beta', true)).toBe(false);
    } finally {
      client.stop();
    }
  });
});
