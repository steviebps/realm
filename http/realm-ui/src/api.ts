import {
  ApiKey,
  Catalog,
  Chamber,
  ChamberResponse,
  CreatedKey,
  KeyAccess,
  Lineage,
  ListResponse,
  Me,
  Revision,
  WireChamber,
} from './models/response';
import { withoutNullFlags } from './utils/flags';
import { encodePath, ensureTrailingSlash } from './utils/path';
import { nullFlagsIn, type NullFlag } from './utils/provenance';
import { messageOf } from './utils/strings';

const ACTOR_KEY = 'realm.actor';

/** The name recorded in change history when the server has no sign-in. Stored per browser. */
export const getActor = (): string => {
  try {
    return localStorage.getItem(ACTOR_KEY) ?? '';
  } catch {
    return '';
  }
};

export const setActor = (actor: string) => {
  try {
    localStorage.setItem(ACTOR_KEY, actor);
  } catch {
    // storage unavailable: the actor is simply not remembered
  }
};

/** Thrown when a save is rejected because the chamber changed underneath the editor. */
export class ConflictError extends Error {
  constructor() {
    super('This chamber was changed by someone else since you opened it.');
  }
}

/** Thrown when the server needs the user to sign in (again). */
export class UnauthorizedError extends Error {
  constructor() {
    super('Your session has ended. Sign in again to continue.');
  }
}

/** Thrown when the caller may not do what a request asks, such as read a chamber outside their paths. */
export class ForbiddenError extends Error {}

/** Dispatched on window whenever a request is refused for lack of a session. */
export const UNAUTHORIZED_EVENT = 'realm:unauthorized';

const errorFrom = async (res: Response) => {
  if (res.status === 401) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    return new UnauthorizedError();
  }
  const Failure = res.status === 403 ? ForbiddenError : Error;
  try {
    const body = (await res.json()) as { errors?: string[] };
    return new Failure(body.errors?.join('; ') || res.statusText);
  } catch {
    return new Failure(res.statusText);
  }
};

/** Returns the data in a response's envelope, or throws the error it carries. */
const dataFrom = async <T>(res: Response): Promise<T> => {
  if (!res.ok) {
    throw await errorFrom(res);
  }
  return ((await res.json()) as { data: T }).data;
};

const apiURL = (route: string, path: string, query = '') =>
  `/v1/${route}${encodePath(ensureTrailingSlash(path))}${query}`;
const chamberURL = (path: string, query = '') => apiURL('chambers', path, query);
const historyURL = (path: string, query = '') => apiURL('history', path, query);

export const listChambers = async (path: string): Promise<string[]> => {
  const res = await fetch(chamberURL(path, '?list=true'));
  if (res.status === 401 || res.status === 403) {
    throw await errorFrom(res);
  }
  const body = (await res.json()) as ListResponse;
  return body.data ?? [];
};

export type LoadedChamber = {
  /** The chamber as stored at this path; this is what gets edited. */
  own: Chamber | null;
  etag: string | null;
  /** The chamber and the parent chambers it inherits from, or null when they could not be loaded. */
  lineage: Lineage | null;
  /** Why the lineage could not be loaded: a ForbiddenError when the caller may not read every parent chamber. */
  lineageError: Error | null;
  /** The flags the stored chamber writes as null, which own leaves out and saving it removes. */
  nullFlags: NullFlag[];
};

const OLDER_SERVER = "This server doesn't report where inherited flags come from. Update it to see parent chambers.";

/**
 * Reads the lineage in a response to `?inherit=layers`. It never throws: a failure is returned, so that the
 * chamber stays editable without it.
 */
const lineageFrom = async (res: Response): Promise<Pick<LoadedChamber, 'lineage' | 'lineageError'>> => {
  try {
    const lineage = await dataFrom<Lineage | undefined>(res);
    // A server that predates ?inherit=layers answers with the inherited chamber instead.
    if (typeof lineage?.inheritable !== 'boolean' || !Array.isArray(lineage.layers) || lineage.layers.length === 0) {
      return { lineage: null, lineageError: new Error(OLDER_SERVER) };
    }
    return { lineage, lineageError: null };
  } catch (err) {
    return { lineage: null, lineageError: err instanceof Error ? err : new Error(messageOf(err)) };
  }
};

/**
 * The chamber own, read with etag, and its lineage, as the UI holds them: without the flags written as null, which
 * writes refuse, and with what removing those own writes changes, as saving it does.
 */
const loaded = (
  own: WireChamber | null | undefined,
  etag: string | null,
  { lineage, lineageError }: Pick<LoadedChamber, 'lineage' | 'lineageError'>
): LoadedChamber => ({
  own: withoutNullFlags(own),
  etag,
  lineage: lineage && {
    ...lineage,
    layers: lineage.layers.map((l) => (l.rules ? { ...l, rules: withoutNullFlags({ rules: l.rules })?.rules } : l)),
  },
  lineageError,
  nullFlags: nullFlagsIn(own, lineage),
});

/**
 * Loads the chamber at path for editing: as stored, with its ETag, and the lineage it inherits from, all from one
 * read when the server sends the ETag with the lineage. Otherwise (an older server, a chamber that cannot be parsed,
 * or parents the caller may not read) it also reads the chamber on its own, and the lineage may then be from another
 * moment.
 */
export const getChamber = async (path: string): Promise<LoadedChamber> => {
  const layersRes = await fetch(chamberURL(path, '?inherit=layers'));
  if (layersRes.status === 404) {
    return { own: null, etag: null, lineage: null, lineageError: null, nullFlags: [] };
  }
  const etag = layersRes.headers.get('ETag');
  const lineage = await lineageFrom(layersRes);
  const here = lineage.lineage?.layers[0];
  if (etag && here?.status === 'ok') {
    return loaded({ rules: here.rules ?? {} }, etag, lineage);
  }
  const ownRes = await fetch(chamberURL(path, '?inherit=false'));
  if (ownRes.status === 404) {
    return { own: null, etag: null, lineage: null, lineageError: null, nullFlags: [] };
  }
  if (!ownRes.ok) {
    throw await errorFrom(ownRes);
  }
  const own = (await ownRes.json()) as ChamberResponse;
  return loaded(own.data, ownRes.headers.get('ETag'), lineage);
};

// The server records an unset actor as anonymous.
const writeHeaders = (precondition: Record<string, string>) => ({
  'Content-Type': 'application/json',
  ...(getActor() ? { 'X-Realm-Actor': getActor() } : {}),
  ...precondition,
});

/** Replaces the chamber at path. Pass the ETag it was loaded with, or null to create it. */
export const saveChamber = async (path: string, chamber: Chamber, etag: string | null) => {
  const res = await fetch(chamberURL(path), {
    method: 'POST',
    body: JSON.stringify(chamber),
    headers: writeHeaders(etag ? { 'If-Match': etag } : { 'If-None-Match': '*' }),
  });
  if (res.status === 412) {
    throw new ConflictError();
  }
  if (!res.ok) {
    throw await errorFrom(res);
  }
};

export const createChamber = (path: string) => saveChamber(path, { rules: {} }, null);

export const getHistory = async (path: string) =>
  (await dataFrom<Revision[] | undefined>(await fetch(historyURL(path)))) ?? [];

export const getRevision = async (path: string, revision: number): Promise<Revision> => {
  const rev = await dataFrom<Revision & { chamber?: WireChamber | null }>(
    await fetch(historyURL(path, `?revision=${revision}`))
  );
  return { ...rev, chamber: withoutNullFlags(rev.chamber) };
};

export const getCatalog = async (): Promise<Catalog> => {
  const res = await fetch('/v1/catalog');
  if (res.status === 401) {
    throw await errorFrom(res);
  }
  if (!res.ok) {
    return {};
  }
  return ((await res.json()) as { data?: Catalog }).data ?? {};
};

export const getMe = async () => dataFrom<Me>(await fetch('/v1/auth/me'));

/** Where to send the browser to sign in with a provider, returning here afterwards. */
export const signInURL = (provider: string) =>
  `/v1/auth/login/${encodeURIComponent(provider)}?returnTo=${encodeURIComponent(window.location.pathname + window.location.search)}`;

export const signOut = async () => {
  await fetch('/v1/auth/logout', { method: 'POST' });
};

export const listKeys = async () => (await dataFrom<ApiKey[] | undefined>(await fetch('/v1/keys'))) ?? [];

export const createKey = async (key: { name: string; access: KeyAccess; path: string }) =>
  dataFrom<CreatedKey>(
    await fetch('/v1/keys', {
      method: 'POST',
      body: JSON.stringify(key),
      headers: { 'Content-Type': 'application/json' },
    })
  );

export const revokeKey = async (id: string) => {
  const res = await fetch(`/v1/keys/${encodeURIComponent(id)}`, { method: 'DELETE' });
  if (!res.ok) {
    throw await errorFrom(res);
  }
};
