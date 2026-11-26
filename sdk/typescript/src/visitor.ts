// A visitor ID is a random ID that stays with one browser, so that visitors
// who are not signed in (who have no user ID to be a context's key) can be
// bucketed into percentage rollouts, and stay in the same bucket from one
// page to the next. It lives in a first-party cookie, which both the server
// (serverVisitorId) and the browser (browserVisitorId) read, so a page
// rendered on the server and the browser that hydrates it agree on it.

/** Options for the cookie a visitor ID is kept in. */
export interface VisitorIdOptions {
  /** The cookie's name. Default `'realm_visitor'`. */
  cookieName?: string;
  /**
   * The cookie's `Domain`, to share one ID with subdomains (`example.com`
   * covers `www.example.com` and `app.example.com`). Default: none, so only
   * the host that set the cookie receives it.
   */
  domain?: string;
  /**
   * How long the cookie lasts, in seconds: a positive integer. Default 400
   * days, the longest browsers keep a cookie.
   */
  maxAge?: number;
  /**
   * Whether the cookie is sent only over HTTPS (`Secure`). Default `true`
   * for serverVisitorId, and for browserVisitorId whether the page was
   * served over HTTPS. Pass `false` to serverVisitorId on a server that
   * serves plain HTTP, as in development: some browsers drop a `Secure`
   * cookie that arrives over HTTP, so every page would get a new ID.
   */
  secure?: boolean;
}

/** A request's visitor ID, from serverVisitorId. */
export interface Visitor {
  /** The visitor ID: the one the request's cookie holds, or a new one. */
  id: string;
  /**
   * When the ID is new, a `Set-Cookie` header value that keeps it in the
   * browser: send it with the response. Undefined when the request already
   * had an ID.
   */
  setCookie?: string;
}

/**
 * The visitor ID of a request, read from its `Cookie` header, or a new one if
 * it has none. A new ID comes with a `Set-Cookie` header value to send with
 * the response, so the browser sends the same ID with its next request and
 * browserVisitorId reads it too. Call it where the response's headers can
 * still be set, such as before rendering a page or in middleware.
 *
 * Only an ID this function or browserVisitorId could have written is
 * accepted: a cookie holding anything else is replaced with a new ID. It
 * throws a TypeError for invalid options.
 */
export function serverVisitorId(cookieHeader: string | null | undefined, options: VisitorIdOptions = {}): Visitor {
  const cookie = visitorCookie(options);
  const id = readVisitorId(cookieHeader ?? '', cookie.name);
  if (id !== undefined) return { id };
  const created = newVisitorId();
  return { id: created, setCookie: cookie.serialize(created, options.secure ?? true) };
}

// createdHere holds the ID browserVisitorId created for each cookie name, so
// that where the browser keeps no cookies, every call made by this page
// still returns the same ID.
const createdHere = new Map<string, string>();

/**
 * The visitor ID of this browser, read from `document.cookie`, or a new one,
 * written there, if it has none. Where the browser keeps no cookies, the new
 * ID lasts as long as the page: every call returns it, but the next page gets
 * another.
 *
 * Some browsers (Safari) keep a cookie written by script for at most seven
 * days, so a visitor who stays away longer gets a new ID; an ID that
 * serverVisitorId set lasts as long as `maxAge`, and is never rewritten
 * here. Where there is no document, as in server rendering, it throws: use
 * serverVisitorId there. It throws a TypeError for invalid options.
 */
export function browserVisitorId(options: VisitorIdOptions = {}): string {
  const cookie = visitorCookie(options);
  const doc = (globalThis as { document?: Document }).document;
  if (doc === undefined) throw new Error('browserVisitorId needs a document; on a server, use serverVisitorId');
  const id = readVisitorId(documentCookie(doc), cookie.name);
  if (id !== undefined) return id;
  let created = createdHere.get(cookie.name);
  if (created === undefined) createdHere.set(cookie.name, (created = newVisitorId()));
  const secure = options.secure ?? ((globalThis as { location?: Location }).location?.protocol === 'https:');
  try {
    doc.cookie = cookie.serialize(created, secure);
  } catch {
    // A sandboxed document refuses cookies; the ID lasts as long as the page.
  }
  return created;
}

// documentCookie returns the document's cookies, or none where reading them
// throws (as in a sandboxed iframe).
function documentCookie(doc: Document): string {
  try {
    return doc.cookie;
  } catch {
    return '';
  }
}

// A visitor ID is a random (version 4) UUID, in lower case.
const visitorIdPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

// readVisitorId returns the first visitor ID among the values of the cookie
// name in header (a Cookie header, or document.cookie), if there is one.
function readVisitorId(header: string, name: string): string | undefined {
  for (const pair of header.split(';')) {
    const eq = pair.indexOf('=');
    if (eq < 0 || pair.slice(0, eq).trim() !== name) continue;
    const value = pair.slice(eq + 1).trim();
    if (visitorIdPattern.test(value)) return value;
  }
  return undefined;
}

// newVisitorId returns a random (version 4) UUID.
function newVisitorId(): string {
  const b = randomBytes(16);
  b[6] = (b[6]! & 0x0f) | 0x40; // version 4
  b[8] = (b[8]! & 0x3f) | 0x80; // the RFC 9562 variant
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

// randomBytes returns n random bytes from Web Crypto's getRandomValues
// (not randomUUID, which browsers offer only to pages served over HTTPS).
// Where there is no Web Crypto, as in Node 18 (without
// --experimental-global-webcrypto), they come from Math.random: a visitor ID
// need only be unique, not secret, since a browser can send any ID it likes.
function randomBytes(n: number): Uint8Array {
  const b = new Uint8Array(n);
  const webCrypto = (globalThis as { crypto?: Partial<Crypto> }).crypto;
  if (typeof webCrypto?.getRandomValues === 'function') return webCrypto.getRandomValues(b);
  for (let i = 0; i < n; i++) b[i] = Math.floor(Math.random() * 256);
  return b;
}

const defaultMaxAge = 400 * 24 * 60 * 60;
// A cookie name is an RFC 9110 token; a domain, a host name, optionally
// with a leading dot.
const cookieNamePattern = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const domainPattern = /^\.?[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*$/;

// visitorCookie checks options, and returns the cookie's name and how to
// write it.
function visitorCookie(options: VisitorIdOptions): { name: string; serialize(id: string, secure: boolean): string } {
  const name = options.cookieName ?? 'realm_visitor';
  if (!cookieNamePattern.test(name)) throw new TypeError(`invalid visitor ID cookie name ${JSON.stringify(name)}`);
  const { domain } = options;
  if (domain !== undefined && !domainPattern.test(domain)) {
    throw new TypeError(`invalid visitor ID cookie domain ${JSON.stringify(domain)}`);
  }
  const maxAge = options.maxAge ?? defaultMaxAge;
  if (!Number.isSafeInteger(maxAge) || maxAge <= 0) {
    throw new TypeError(`invalid visitor ID cookie maxAge ${String(maxAge)}: it must be a positive integer`);
  }
  return {
    name,
    serialize(id, secure) {
      let cookie = `${name}=${id}; Path=/; Max-Age=${maxAge}; SameSite=Lax`;
      if (domain !== undefined) cookie += `; Domain=${domain}`;
      if (secure) cookie += '; Secure';
      return cookie;
    },
  };
}
