// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { browserVisitorId, serverVisitorId, type VisitorIdOptions } from '../src/index.js';

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const valid = '0b7e3c6a-58d1-4f2e-9a4b-3c2d1e0f9a8b';
const defaults = 'Path=/; Max-Age=34560000; SameSite=Lax';

afterEach(() => {
  vi.unstubAllGlobals();
});

// fakeDocument stands in for the page's document: it records what is written
// to its cookie, and keeps it unless the browser drops cookies (keeps false),
// or refuses them altogether (refuses true, as a sandboxed iframe does).
function fakeDocument(cookie = '', { keeps = true, refuses = false } = {}) {
  const writes: string[] = [];
  let jar = cookie;
  const refuse = () => {
    throw new DOMException('The document is sandboxed and lacks the "allow-same-origin" flag.', 'SecurityError');
  };
  vi.stubGlobal('document', {
    get cookie() {
      return refuses ? refuse() : jar;
    },
    set cookie(value: string) {
      if (refuses) refuse();
      writes.push(value);
      if (keeps) jar = [jar, value.split(';')[0]].filter(Boolean).join('; ');
    },
  });
  return writes;
}

describe('serverVisitorId', () => {
  it.each([
    ['the ID among other cookies', `a=1; realm_visitor=${valid}; b=2`],
    ['the ID, with spaces around it', ` realm_visitor = ${valid} `],
    ['the first ID among cookies of that name', `realm_visitor=hello; realm_visitor=${valid}; realm_visitor=${uuidLike(2)}`],
  ])('reads %s', (_name, header) => {
    expect(serverVisitorId(header)).toEqual({ id: valid });
  });

  it.each([
    ['no header', undefined],
    ['a null header', null],
    ['no cookie', ''],
    ['another cookie', 'a=1; b=2'],
    ['a cookie whose name ends in it', `xrealm_visitor=${valid}`],
    ['a value that is not a UUID', 'realm_visitor=hello'],
    ['an upper-case UUID', `realm_visitor=${valid.toUpperCase()}`],
    ['a UUID of another version', 'realm_visitor=0b7e3c6a-58d1-1f2e-9a4b-3c2d1e0f9a8b'],
    ['a UUID with more after it', `realm_visitor=${valid}0`],
    ['a quoted UUID', `realm_visitor="${valid}"`],
  ])('creates an ID for a request with %s, and a cookie to keep it', (_name, header) => {
    const visitor = serverVisitorId(header);
    expect(visitor.id).toMatch(uuid);
    expect(visitor.id).not.toBe(valid);
    expect(visitor.setCookie).toBe(`realm_visitor=${visitor.id}; ${defaults}; Secure`);
  });

  it.each<[VisitorIdOptions, string]>([
    [{ cookieName: 'vid' }, `vid=ID; ${defaults}; Secure`],
    [{ domain: 'example.com' }, `realm_visitor=ID; ${defaults}; Domain=example.com; Secure`],
    [{ domain: '.example.com' }, `realm_visitor=ID; ${defaults}; Domain=.example.com; Secure`],
    [{ maxAge: 3600 }, 'realm_visitor=ID; Path=/; Max-Age=3600; SameSite=Lax; Secure'],
    [{ secure: false }, `realm_visitor=ID; ${defaults}`],
  ])('writes the cookie as %j says', (options, want) => {
    const visitor = serverVisitorId(undefined, options);
    expect(visitor.setCookie).toBe(want.replace('ID', visitor.id));
  });

  it('reads the cookie the cookieName option names', () => {
    expect(serverVisitorId(`realm_visitor=${uuidLike(1)}; vid=${valid}`, { cookieName: 'vid' })).toEqual({ id: valid });
  });

  it.each<VisitorIdOptions>([
    { cookieName: '' },
    { cookieName: 'a b' },
    { cookieName: 'a;b' },
    { cookieName: 'a=b' },
    { domain: '' },
    { domain: 'example.com; Secure' },
    { domain: 'example..com' },
    { maxAge: 0 },
    { maxAge: -1 },
    { maxAge: 1.5 },
    { maxAge: Number.NaN },
    { maxAge: Number.POSITIVE_INFINITY },
  ])('refuses the options %j', (options) => {
    expect(() => serverVisitorId(`realm_visitor=${valid}`, options)).toThrow(TypeError);
    expect(() => browserVisitorId(options)).toThrow(TypeError);
  });

  it.each([
    ['', false],
    [' where there is no Web Crypto, as in Node 18', true],
  ])('creates random version 4 UUIDs%s', (_name, noWebCrypto) => {
    if (noWebCrypto) vi.stubGlobal('crypto', undefined);
    const ids = new Set(Array.from({ length: 1000 }, () => serverVisitorId(undefined).id));
    expect(ids.size).toBe(1000);
    for (const id of ids) expect(id).toMatch(uuid);
  });
});

describe('browserVisitorId', () => {
  it('reads the ID the cookie holds, and writes nothing', () => {
    const writes = fakeDocument(`a=1; realm_visitor=${valid}`);
    expect(browserVisitorId()).toBe(valid);
    expect(writes).toEqual([]);
  });

  it('creates an ID, writes it, and reads it back after', () => {
    const writes = fakeDocument('a=1', { keeps: true });
    const id = browserVisitorId({ cookieName: 'created' });
    expect(id).toMatch(uuid);
    expect(writes).toEqual([`created=${id}; ${defaults}`]);
    expect(browserVisitorId({ cookieName: 'created' })).toBe(id);
    expect(writes).toHaveLength(1);
  });

  it('replaces a cookie holding something else', () => {
    const writes = fakeDocument('replaced=hello');
    const id = browserVisitorId({ cookieName: 'replaced' });
    expect(id).toMatch(uuid);
    expect(writes).toEqual([`replaced=${id}; ${defaults}`]);
  });

  it.each<[string, VisitorIdOptions, string]>([
    ['https:', {}, '; Secure'],
    ['https:', { secure: false }, ''],
    ['http:', {}, ''],
    ['http:', { secure: true }, '; Secure'],
  ])('on a page served over %s with %j, writes the cookie accordingly', (protocol, options, secure) => {
    vi.stubGlobal('location', { protocol });
    const writes = fakeDocument();
    const id = browserVisitorId(options);
    expect(writes).toEqual([`realm_visitor=${id}; ${defaults}${secure}`]);
  });

  it.each<[string, { keeps?: boolean; refuses?: boolean }]>([
    ['drops cookies', { keeps: false }],
    ['refuses cookies', { refuses: true }],
  ])('keeps one ID for the page where the browser %s', (_name, behavior) => {
    const writes = fakeDocument('', behavior);
    const name = `kept-${String(behavior.keeps)}`;
    const id = browserVisitorId({ cookieName: name });
    expect(id).toMatch(uuid);
    expect(browserVisitorId({ cookieName: name })).toBe(id);
    expect(browserVisitorId({ cookieName: `${name}-other` })).not.toBe(id);
    // each call tries to write it again, in case the browser now keeps it
    expect(writes).toHaveLength(behavior.keeps === false ? 3 : 0);
  });

  it('throws where there is no document', () => {
    vi.stubGlobal('document', undefined);
    expect(() => browserVisitorId()).toThrow(/serverVisitorId/);
  });

  it("agrees with serverVisitorId through the browser's cookies", () => {
    // jsdom keeps cookies like a browser: an ID the server set...
    const fromServer = serverVisitorId(document.cookie, { cookieName: 'from-server', secure: false });
    document.cookie = fromServer.setCookie!;
    expect(browserVisitorId({ cookieName: 'from-server' })).toBe(fromServer.id);
    // ...and one the browser created, which the server then receives.
    const fromBrowser = browserVisitorId({ cookieName: 'from-browser' });
    expect(serverVisitorId(document.cookie, { cookieName: 'from-browser' })).toEqual({ id: fromBrowser });
    expect(serverVisitorId(document.cookie, { cookieName: 'from-server' })).toEqual({ id: fromServer.id });
  });
});

// uuidLike returns a valid visitor ID other than valid, differing in its last digit.
function uuidLike(n: number): string {
  return valid.slice(0, -1) + String(n);
}
