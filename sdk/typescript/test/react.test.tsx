// @vitest-environment jsdom
import { StrictMode, type ReactNode } from 'react';
import { createRoot } from 'react-dom/client';
import { renderToString } from 'react-dom/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BrowserClient, localStorageAdapter, type EvaluationContext } from '../src/index.js';
import {
  RealmProvider,
  useAllFlags,
  useBoolFlag,
  useJsonFlag,
  useNumberFlag,
  useRealmClient,
  useRealmStatus,
  useStringFlag,
} from '../src/react.js';
import { fakeRealm, seedStorage } from './fake-realm.js';

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

// render mounts node in a detached element; React applies updates on its own
// schedule, so tests wait for the text they expect.
function render(node: ReactNode) {
  const container = document.createElement('div');
  const root = createRoot(container);
  root.render(node);
  let mounted = true;
  const unmount = () => {
    if (mounted) root.unmount();
    mounted = false;
  };
  cleanup.push(unmount);
  return { text: () => container.textContent, rerender: (next: ReactNode) => root.render(next), unmount };
}

function Beta() {
  const beta = useBoolFlag('beta', false);
  const { ready, error } = useRealmStatus();
  const state = error ? `error: ${error.message}` : ready ? 'ready' : 'loading';
  return <p>{`${state} beta=${beta}`}</p>;
}

function app(client: BrowserClient, context?: EvaluationContext) {
  return (
    <RealmProvider client={client} context={context}>
      <Beta />
    </RealmProvider>
  );
}

describe('RealmProvider', () => {
  it('starts the client, renders changes, and stops on unmount', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, path: '/prod/web/' });
    const view = render(app(client));

    await vi.waitFor(() => expect(view.text()).toBe('ready beta=false'));
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));

    realm.publish(betaOn);
    await vi.waitFor(() => expect(view.text()).toBe('ready beta=true'));

    view.unmount();
    await vi.waitFor(() => expect(realm.streams.size).toBe(0));
    expect(client.bool('beta', false)).toBe(true); // the last values stay
  });

  it('re-evaluates when the context changes by value', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address });
    const view = render(app(client, { key: 'u1' }));
    await vi.waitFor(() => expect(view.text()).toBe('ready beta=false'));
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    expect(realm.requests.map((r) => r.body)).toEqual(['{"key":"u1"}', '{"key":"u1"}']);

    view.rerender(app(client, { key: 'u1' })); // a new object with the same value
    view.rerender(app(client, { key: 'vip' }));
    await vi.waitFor(() => expect(view.text()).toBe('ready beta=true'));
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));
    expect(realm.requests.map((r) => r.body)).toEqual(['{"key":"u1"}', '{"key":"u1"}', '{"key":"vip"}', '{"key":"vip"}']);
  });

  it('leaves one stream open in StrictMode and none after unmount', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address });
    const view = render(<StrictMode>{app(client)}</StrictMode>);
    await vi.waitFor(() => expect(view.text()).toBe('ready beta=false'));
    await vi.waitFor(() => expect(realm.streams.size).toBe(1));

    view.unmount();
    await vi.waitFor(() => expect(realm.streams.size).toBe(0));
  });

  it('reports a failed start', async () => {
    const realm = await setup();
    realm.setStatus(403);
    const client = new BrowserClient({ address: realm.address });
    const view = render(app(client));
    await vi.waitFor(() => expect(view.text()).toBe('error: not allowed beta=false'));
  });

  it('turns ready when a failed start recovers', async () => {
    const realm = await setup();
    realm.setStatus(403);
    const client = new BrowserClient({ address: realm.address, onError: () => {} });
    const view = render(app(client, { key: 'vip' }));
    await vi.waitFor(() => expect(view.text()).toBe('error: not allowed beta=false'));

    realm.setStatus(200);
    await vi.waitFor(() => expect(view.text()).toBe('ready beta=true'), { timeout: 3000 });
  });

  it('is never ready with the previous context\'s values', async () => {
    const realm = await setup();
    const client = new BrowserClient({ address: realm.address, onError: () => {} });
    const seen: string[] = [];
    function Record() {
      const beta = useBoolFlag('beta', false);
      const { ready, error } = useRealmStatus();
      const text = `${error ? `error: ${error.message}` : ready ? 'ready' : 'loading'} beta=${beta}`;
      seen.push(text);
      return <p>{text}</p>;
    }
    const tree = (context: EvaluationContext) => (
      <RealmProvider client={client} context={context}>
        <Record />
      </RealmProvider>
    );
    const view = render(tree({ key: 'vip' }));
    await vi.waitFor(() => expect(view.text()).toBe('ready beta=true'));

    realm.setStatus(403);
    seen.length = 0;
    view.rerender(tree({ key: 'u1' }));
    await vi.waitFor(() => expect(view.text()).toBe('error: not allowed beta=false'));
    expect(seen).not.toContain('ready beta=true');

    realm.setStatus(200);
    await vi.waitFor(() => expect(view.text()).toBe('ready beta=false'), { timeout: 3000 });
  });
});

describe('offline start', () => {
  it('renders stored values for the context before the evaluation, and reports their source', async () => {
    const realm = await setup();
    const storage = localStorageAdapter();
    localStorage.clear();
    cleanup.push(() => localStorage.clear());
    await seedStorage(realm.address, storage, { key: 'vip' });

    function Source() {
      const beta = useBoolFlag('beta', false);
      const { ready, source } = useRealmStatus();
      return <p>{`${ready ? 'ready' : 'loading'} ${source} beta=${beta}`}</p>;
    }
    realm.setHang(true);
    const client = new BrowserClient({ address: realm.address, storage, onError: () => {} });
    const view = render(
      <RealmProvider client={client} context={{ key: 'vip' }}>
        <Source />
      </RealmProvider>,
    );
    await vi.waitFor(() => expect(view.text()).toBe('loading cache beta=true'));

    realm.setHang(false);
    client.stop();
    const online = new BrowserClient({ address: realm.address, storage, bootstrap: { beta: { type: 'boolean', value: true } } });
    view.rerender(
      <RealmProvider client={online}>
        <Source />
      </RealmProvider>,
    );
    await vi.waitFor(() => expect(view.text()).toBe('ready network beta=false'));
  });

  it("never renders another context's stored values", async () => {
    const realm = await setup();
    const storage = localStorageAdapter();
    localStorage.clear();
    cleanup.push(() => localStorage.clear());
    await seedStorage(realm.address, storage, { key: 'vip' });

    // the client's own context has stored values; the provider's does not
    realm.setHang(true);
    const client = new BrowserClient({ address: realm.address, context: { key: 'vip' }, storage, onError: () => {} });
    expect(client.bool('beta', false)).toBe(true);
    const seen: string[] = [];
    function Record() {
      const beta = useBoolFlag('beta', false);
      const { source } = useRealmStatus();
      const text = `${source} beta=${beta}`;
      seen.push(text);
      return <p>{text}</p>;
    }
    const view = render(
      <RealmProvider client={client} context={{ key: 'u1' }}>
        <Record />
      </RealmProvider>,
    );
    await vi.waitFor(() => expect(client.contextKey()).toBe(JSON.stringify({ key: 'u1' })));
    await vi.waitFor(() => expect(view.text()).toBe('undefined beta=false'));
    expect(seen.length).toBeGreaterThan(0);
    expect(seen.every((t) => t === 'undefined beta=false')).toBe(true);
    realm.setHang(false);
  });
});

describe('hooks', () => {
  it('read each type of flag and every flag', async () => {
    const realm = await setup();
    realm.publish({
      rules: {
        beta: { type: 'boolean', value: true },
        banner: { type: 'string', value: 'hello' },
        limit: { type: 'number', value: 5 },
        quota: { type: 'custom', value: { max: 3 } },
      },
    });
    const client = new BrowserClient({ address: realm.address, streaming: false });
    let renders = 0;
    function Flags() {
      renders++;
      const values = [
        useBoolFlag('beta', false),
        useStringFlag('banner', ''),
        useNumberFlag('limit', 0),
        useJsonFlag('quota', { max: 0 }).max, // an inline fallback must not render in a loop
        useStringFlag('beta', 'wrong type'),
        Object.keys(useAllFlags()).length,
        useRealmClient() === client,
      ];
      return <p>{values.join(' ')}</p>;
    }
    const view = render(
      <RealmProvider client={client}>
        <Flags />
      </RealmProvider>,
    );
    await vi.waitFor(() => expect(view.text()).toBe('true hello 5 3 wrong type 4 true'));
    expect(renders).toBeLessThan(5);
  });

  it('render fallbacks on the server', () => {
    const client = new BrowserClient({ address: 'http://127.0.0.1:1' });
    expect(renderToString(app(client))).toBe('<p>loading beta=false</p>');
  });

  it('throw outside a RealmProvider', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    cleanup.push(() => vi.restoreAllMocks());
    expect(() => renderToString(<Beta />)).toThrow('useBoolFlag must be used within a RealmProvider');
  });
});
