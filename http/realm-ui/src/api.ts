import {
  ApiKey,
  Catalog,
  Chamber,
  ChamberResponse,
  CreatedKey,
  KeyAccess,
  ListResponse,
  Me,
  Revision,
} from './models/response';
import { encodePath, ensureTrailingSlash } from './utils/path';

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

/** Dispatched on window whenever a request is refused for lack of a session. */
export const UNAUTHORIZED_EVENT = 'realm:unauthorized';

const errorFrom = async (res: Response) => {
  if (res.status === 401) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    return new UnauthorizedError();
  }
  try {
    const body = (await res.json()) as { errors?: string[] };
    return new Error(body.errors?.join('; ') || res.statusText);
  } catch {
    return new Error(res.statusText);
  }
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
  /** The chamber including rules inherited from parent chambers. */
  merged: Chamber | null;
  etag: string | null;
};

export const getChamber = async (path: string): Promise<LoadedChamber> => {
  // The root chamber has no parents, so its inherited view is identical.
  const isRoot = ensureTrailingSlash(path) === '/';
  const [ownRes, mergedRes] = await Promise.all([
    fetch(chamberURL(path, '?inherit=false')),
    isRoot ? null : fetch(chamberURL(path)),
  ]);
  if (ownRes.status === 404) {
    return { own: null, merged: null, etag: null };
  }
  if (!ownRes.ok) {
    throw await errorFrom(ownRes);
  }
  const own = (await ownRes.json()) as ChamberResponse;
  const merged = mergedRes?.ok ? ((await mergedRes.json()) as ChamberResponse) : own;
  return { own: own.data ?? null, merged: merged.data ?? null, etag: ownRes.headers.get('ETag') };
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

export const getHistory = async (path: string): Promise<Revision[]> => {
  const res = await fetch(historyURL(path));
  if (!res.ok) {
    throw await errorFrom(res);
  }
  return ((await res.json()) as { data?: Revision[] }).data ?? [];
};

export const getRevision = async (path: string, revision: number): Promise<Revision> => {
  const res = await fetch(historyURL(path, `?revision=${revision}`));
  if (!res.ok) {
    throw await errorFrom(res);
  }
  return ((await res.json()) as { data: Revision }).data;
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

export const getMe = async (): Promise<Me> => {
  const res = await fetch('/v1/auth/me');
  if (!res.ok) {
    throw await errorFrom(res);
  }
  return ((await res.json()) as { data: Me }).data;
};

/** Where to send the browser to sign in with a provider, returning here afterwards. */
export const signInURL = (provider: string) =>
  `/v1/auth/login/${encodeURIComponent(provider)}?returnTo=${encodeURIComponent(window.location.pathname + window.location.search)}`;

export const signOut = async () => {
  await fetch('/v1/auth/logout', { method: 'POST' });
};

export const listKeys = async (): Promise<ApiKey[]> => {
  const res = await fetch('/v1/keys');
  if (!res.ok) {
    throw await errorFrom(res);
  }
  return ((await res.json()) as { data?: ApiKey[] }).data ?? [];
};

export const createKey = async (key: { name: string; access: KeyAccess; path: string }): Promise<CreatedKey> => {
  const res = await fetch('/v1/keys', {
    method: 'POST',
    body: JSON.stringify(key),
    headers: { 'Content-Type': 'application/json' },
  });
  if (!res.ok) {
    throw await errorFrom(res);
  }
  return ((await res.json()) as { data: CreatedKey }).data;
};

export const revokeKey = async (id: string) => {
  const res = await fetch(`/v1/keys/${encodeURIComponent(id)}`, { method: 'DELETE' });
  if (!res.ok) {
    throw await errorFrom(res);
  }
};
