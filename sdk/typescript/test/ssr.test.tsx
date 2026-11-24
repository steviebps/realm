// @vitest-environment jsdom
import { act, type ReactNode } from 'react';
import { createRoot, hydrateRoot } from 'react-dom/client';
import { renderToString } from 'react-dom/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  BrowserClient,
  serializeInitialState,
  ServerClient,
  type EvaluationContext,
  type InitialState,
  type StorageAdapter,
} from '../src/index.js';
import { RealmProvider, useAllFlags, useBoolFlag, useJsonFlag, useRealmStatus, useStringFlag } from '../src/react.js';
import { storageKey } from '../src/storage.js';
import { fakeRealm, memoryStorage } from './fake-realm.js';

// The fake server turns every flag on for the key `vip`; this chamber's
// rules say the same, so a ServerClient and the fake's evaluation agree.
const chamber = {
  rules: {
    beta: {
      type: 'boolean',
      value: false,
      targeting: [{ clauses: [{ attribute: 'key', op: 'in', values: ['vip'] }], value: true }],
    },
  },
};

// Tells React that tests wrap updates in act.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const betaOn = { rules: { beta: { type: 'boolean', value: true } } };
const betaOff = { rules: { beta: { type: 'boolean', value: false } } };

let cleanup: (() => unknown)[] = [];
afterEach(async () => {
  for (const fn of cleanup.reverse()) await fn();
  cleanup = [];
  vi.restoreAllMocks();
});

// setup serves rules (at revision, if given) from a fake server, and starts
// a ServerClient on them.
async function setup(rules: unknown = chamber, revision?: string) {
  const realm = await fakeRealm();
  cleanup.push(realm.close);
  realm.publish(rules);
  realm.setRevision(revision);
  const server = new ServerClient({ address: realm.address, path: '/prod/web/', streaming: false });
  cleanup.push(() => server.stop());
  await server.start();
  return { realm, server };
}

// seedStale stores an evaluation for context with beta off, as if from
// before a change, where a client of realm's /prod/web/ reads it.
function seedStale(storage: ReturnType<typeof memoryStorage>, address: string, context: EvaluationContext) {
  storage.items.set(
    storageKey(new URL('/v1/evaluate/prod/web/', address).toString(), context),
    JSON.stringify({ flags: { beta: { type: 'boolean', value: false } } }),
  );
}

function Beta() {
  const beta = useBoolFlag('beta', false);
  const { ready, source } = useRealmStatus();
  return <p>{`${ready ? 'ready' : 'loading'} ${source ?? 'none'} beta=${beta}`}</p>;
}

function app(client: BrowserClient, context?: EvaluationContext, children: ReactNode = <Beta />) {
  return (
    <RealmProvider client={client} context={context}>
      {children}
    </RealmProvider>
  );
}

// hydrate renders node on the "server" and hydrates the markup in a
// container, as a browser would, failing on any hydration mismatch.
async function hydrate(server: () => ReactNode, browser: () => ReactNode) {
  const html = renderToString(server());
  const container = document.createElement('div');
  container.innerHTML = html;
  const errors: unknown[] = [];
  vi.spyOn(console, 'error').mockImplementation((...args) => void errors.push(args));
  let root: ReturnType<typeof hydrateRoot> | undefined;
  await act(async () => {
    root = hydrateRoot(container, browser(), { onRecoverableError: (e) => void errors.push(e) });
  });
  cleanup.push(() => act(() => root!.unmount()));
  return { html, text: () => container.textContent, errors };
}

describe('ServerClient.initialState', () => {
  it('evaluates the context as the evaluation endpoint receives it', async () => {
    const { server } = await setup();
    const withDefaults = new ServerClient({
      address: 'http://127.0.0.1:1',
      attributes: { plan: 'pro' },
      version: '2.0.0',
    });
    // process-wide attributes are not the browser's, so they are not applied
    expect(withDefaults.initialState({ key: 'vip' })).toEqual({ context: { key: 'vip' }, flags: {} });

    const context = { key: 42, attributes: { build: 7, paid: true, tags: ['a'] } } as unknown as EvaluationContext;
    expect(server.initialState(context)).toEqual({
      context: { key: '42', attributes: { build: '7', paid: 'true' } },
      flags: { beta: { type: 'boolean', value: false } },
    });
    expect(server.initialState({ key: 'vip' }).flags).toEqual({ beta: { type: 'boolean', value: true } });
    expect(server.initialState()).toEqual({ context: {}, flags: { beta: { type: 'boolean', value: false } } });
  });

  it('carries the chamber revision', async () => {
    const { server } = await setup(chamber, '41');
    expect(server.initialState({ key: 'vip' }).revision).toBe(41);
  });
});

describe('serializeInitialState', () => {
  it('escapes what could end a script element, and parses back', () => {
    const state: InitialState = {
      context: { key: '</script><script>alert(1)</script>', attributes: { note: 'a & b <!-- \u2028\u2029' } },
      flags: { beta: { type: 'string', value: '<b>' } },
    };
    const text = serializeInitialState(state);
    expect(text).not.toMatch(/[<>&\u2028\u2029]/);
    expect(JSON.parse(text)).toEqual(state);
    // eslint-disable-next-line no-new-func
    expect(new Function(`return ${text}`)()).toEqual(state);
  });
});

describe('BrowserClient initialState', () => {
  it('serves the values as ready, for its context, until the evaluation arrives', async () => {
    const { realm, server } = await setup();
    realm.setRevision('7');
    const state = server.initialState({ key: 'vip' });
    const client = new BrowserClient({ address: realm.address, path: '/prod/web/', initialState: state, streaming: false });
    cleanup.push(() => client.stop());
    expect(client.contextKey()).toBe(JSON.stringify({ key: 'vip' }));
    expect(client.source()).toBe('render');
    expect(client.isReady()).toBe(true);
    expect(client.bool('beta', false)).toBe(true);
    expect(client.revision()).toBeUndefined(); // the ServerClient read before the revision was set

    await client.start();
    expect(client.source()).toBe('network');
    expect(client.bool('beta', false)).toBe(true);
    expect(client.revision()).toBe(7);
  });

  it('matches contexts as the server receives them', async () => {
    const state: InitialState = {
      context: { key: '42', attributes: { a: '1', b: 'true' } },
      flags: { beta: { type: 'boolean', value: true } },
      revision: 3,
    };
    const same = { key: 42, attributes: { b: true, a: 1 } } as unknown as EvaluationContext;
    const client = new BrowserClient({ address: 'http://127.0.0.1:1', context: same, initialState: state });
    expect(client.source()).toBe('render');
    expect(client.revision()).toBe(3);

    const other = new BrowserClient({ address: 'http://127.0.0.1:1', context: { key: '43' }, initialState: state });
    expect(other.source()).toBeUndefined();
    expect(other.bool('beta', false)).toBe(false);
    await other.identify(same); // a stopped client only switches context
    expect(other.source()).toBe('render');
  });

  it('wins over stored values until the evaluation arrives, then never again', async () => {
    const { realm, server } = await setup();
    const storage = memoryStorage();
    const vip = { key: 'vip' };
    // storage holds an older evaluation, from before beta was on for vip
    seedStale(storage, realm.address, vip);
    const state = server.initialState(vip);
    const opts = { address: realm.address, path: '/prod/web/', storage, streaming: false } as const;
    const client = new BrowserClient({ ...opts, initialState: state });
    cleanup.push(() => client.stop());
    expect(client.source()).toBe('render');
    expect(client.bool('beta', false)).toBe(true);

    await client.start();
    expect(client.source()).toBe('network');
    client.stop();

    // the server's own evaluation is newer than the render's from now on
    realm.publish({ rules: { beta: { type: 'boolean', value: false } } });
    await client.identify({ key: 'u1' });
    await client.start();
    expect(client.bool('beta', true)).toBe(false);
    client.stop();
    await client.identify(vip);
    expect(client.source()).toBe('cache');
    expect(client.isReady()).toBe(false);
  });

  it('is not stored, and serves as bootstrap values once superseded without storage', async () => {
    const { realm, server } = await setup();
    const storage = memoryStorage();
    const state = server.initialState({ key: 'vip' });
    const client = new BrowserClient({ address: realm.address, path: '/prod/web/', initialState: state, streaming: false });
    cleanup.push(() => client.stop());
    const stored = new BrowserClient({ address: 'http://127.0.0.1:1', initialState: state, storage });
    expect(stored.source()).toBe('render');
    expect(storage.items.size).toBe(0);

    await client.start();
    client.stop();
    await client.identify({ key: 'u1' });
    await client.identify({ key: 'vip' });
    expect(client.source()).toBe('bootstrap');
    expect(client.isReady()).toBe(false);
    expect(client.bool('beta', false)).toBe(true);
  });
});

describe('server rendering', () => {
  it('renders the initial state, ready, on the server', async () => {
    const { realm, server } = await setup();
    const context = { key: 'vip' };
    const flags = new BrowserClient({ address: realm.address, path: '/prod/web/', initialState: server.initialState(context) });
    expect(renderToString(app(flags, context))).toBe('<p>ready render beta=true</p>');
    expect(renderToString(app(flags))).toBe('<p>ready render beta=true</p>');
    // never another context's values
    expect(renderToString(app(flags, { key: 'u1' }))).toBe('<p>loading none beta=false</p>');

    // the context option, written another way, still gets the state
    const state: InitialState = { context: { key: '1', attributes: { a: 'x', b: 'y' } }, flags: { beta: { type: 'boolean', value: true } } };
    const reordered = new BrowserClient({
      address: realm.address,
      context: { attributes: { b: 'y', a: 'x' }, key: 1 } as unknown as EvaluationContext,
      initialState: state,
    });
    expect(renderToString(app(reordered))).toBe('<p>ready render beta=true</p>');
    expect(realm.requests.filter((r) => r.url.startsWith('/v1/evaluate/'))).toHaveLength(0);
  });

  it('renders bootstrap values on the server', () => {
    const flags = new BrowserClient({
      address: 'http://127.0.0.1:1',
      context: { key: 'u1' },
      bootstrap: { beta: { type: 'boolean', value: true } },
    });
    expect(renderToString(app(flags, { key: 'u1' }))).toBe('<p>loading bootstrap beta=true</p>');
    expect(renderToString(app(flags, { key: 'u2' }))).toBe('<p>loading none beta=false</p>');
  });

  it('renders every hook from the initial state', async () => {
    const state: InitialState = {
      context: { key: 'u1' },
      flags: {
        beta: { type: 'boolean', value: true },
        theme: { type: 'string', value: 'dark' },
        quota: { type: 'custom', value: { max: 3 } },
      },
    };
    function All() {
      const theme = useStringFlag('theme', 'light');
      const quota = useJsonFlag('quota', { max: 0 });
      const all = useAllFlags();
      return <p>{`${theme} ${quota.max} ${Object.keys(all).join(',')}`}</p>;
    }
    const flags = new BrowserClient({ address: 'http://127.0.0.1:1', initialState: state });
    expect(renderToString(app(flags, undefined, <All />))).toBe('<p>dark 3 beta,theme,quota</p>');
  });

  it('hydrates without a mismatch, then follows the server', async () => {
    const { realm, server } = await setup(betaOn);
    const context = { key: 'u1', attributes: { tenantId: 't1' } };
    const state = server.initialState(context);
    // the state crosses to the browser as text
    const sent = JSON.parse(serializeInitialState(state)) as InitialState;
    // the browser has a stale stored value for the context, which must not
    // show through
    const storage = memoryStorage();
    seedStale(storage, realm.address, context);
    const make = (initialState: InitialState, s?: StorageAdapter) =>
      new BrowserClient({ address: realm.address, path: '/prod/web/', initialState, storage: s });
    const browser = make(sent, storage);
    const seen: string[] = [];
    function Record() {
      const beta = useBoolFlag('beta', false);
      seen.push(`beta=${beta}`);
      return <Beta />;
    }
    // the browser writes the context with its attributes in another order
    const view = await hydrate(
      () => app(make(state), { attributes: { tenantId: 't1' }, key: 'u1' }, <Record />),
      () => app(browser, { key: 'u1', attributes: { tenantId: 't1' } }, <Record />),
    );
    expect(view.html).toBe('<p>ready render beta=true</p>');
    expect(view.errors).toEqual([]);
    await vi.waitFor(() => expect(view.text()).toBe('ready network beta=true'));
    expect(seen).not.toContain('beta=false');

    realm.publish(betaOff);
    await vi.waitFor(() => expect(view.text()).toBe('ready network beta=false'));
  });

  it('hydrates late content with the rendered values after the evaluation arrived', async () => {
    const { realm, server } = await setup();
    const state = server.initialState({ key: 'u1' });
    const browser = new BrowserClient({ address: realm.address, path: '/prod/web/', initialState: state, streaming: false });
    cleanup.push(() => browser.stop());
    // the server's rules change, and the browser has their evaluation
    // before this part of the page hydrates
    realm.publish(betaOn);
    await browser.start();
    expect(browser.bool('beta', false)).toBe(true);
    browser.stop();

    const serverFlags = new BrowserClient({ address: realm.address, path: '/prod/web/', initialState: state });
    const view = await hydrate(
      () => app(serverFlags),
      () => app(browser),
    );
    expect(view.html).toBe('<p>ready render beta=false</p>');
    expect(view.errors).toEqual([]);
    // after hydration, the newer values show
    await vi.waitFor(() => expect(view.text()).toBe('ready network beta=true'));
  });

  it('serves the initial state without hydration too', async () => {
    const { realm, server } = await setup();
    const flags = new BrowserClient({ address: realm.address, path: '/prod/web/', initialState: server.initialState({ key: 'vip' }) });
    const container = document.createElement('div');
    const root = createRoot(container);
    const seen: string[] = [];
    function Record() {
      const text = `beta=${useBoolFlag('beta', false)} ready=${useRealmStatus().ready}`;
      seen.push(text);
      return <p>{text}</p>;
    }
    await act(async () => root.render(app(flags, { key: 'vip' }, <Record />)));
    cleanup.push(() => act(() => root.unmount()));
    expect(seen[0]).toBe('beta=true ready=true');
    expect(seen).not.toContain('beta=false ready=false');
  });
});
