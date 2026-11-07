import { isFlagType } from './evaluate.js';
import { sha1 } from './sha1.js';
import type { EvaluatedFlags, EvaluationContext } from './types.js';

const encoder = new TextEncoder();

/**
 * Where a BrowserClient keeps the values it last received, so that a reload
 * without a network starts from them instead of from fallbacks. Its methods
 * may be synchronous (like localStorage) or return promises (like
 * IndexedDB). They may throw or reject; the client reports that to onError
 * and carries on without the cache.
 */
export interface StorageAdapter {
  /** The value stored under key, or null (or undefined) if there is none. */
  getItem(key: string): string | null | undefined | Promise<string | null | undefined>;
  /** Stores value under key, replacing any earlier one. */
  setItem(key: string, value: string): void | Promise<void>;
  /**
   * Removes the value stored under key. A client keeps the values of only the
   * few contexts it used most recently, and removes the others' with it;
   * without it, it overwrites them with an empty string.
   */
  removeItem?(key: string): void | Promise<void>;
}

/**
 * A StorageAdapter over the browser's localStorage (or another Web Storage
 * area, such as sessionStorage). Where there is no such storage, as in
 * server rendering or when the browser blocks it, it stores nothing.
 */
export function localStorageAdapter(storage?: Storage): StorageAdapter {
  const area = (): Storage | undefined => {
    if (storage) return storage;
    try {
      // Reading localStorage throws where the browser forbids storage.
      return (globalThis as { localStorage?: Storage }).localStorage;
    } catch {
      return undefined;
    }
  };
  return {
    getItem: (key) => area()?.getItem(key),
    setItem: (key, value) => area()?.setItem(key, value),
    removeItem: (key) => area()?.removeItem(key),
  };
}

/** How many contexts' values a client keeps in storage for each chamber. */
export const maxStoredContexts = 10;

// The start of every storage key for the chamber at evaluateUrl.
function prefix(evaluateUrl: string): string {
  return `realm:v1:${evaluateUrl}:`;
}

/**
 * The key a client's values for context are stored under. It names the
 * evaluate URL (server and chamber) and a hash of the context, so values for
 * one user are never read back for another, and the context's attributes are
 * not written in the clear.
 */
export function storageKey(evaluateUrl: string, context: EvaluationContext): string {
  return prefix(evaluateUrl) + hex(sha1(encoder.encode(canonicalContext(context))));
}

/**
 * The key of the list of contexts whose values storage holds for the chamber
 * at evaluateUrl (the hashes in their keys), most recently used first.
 */
export function recentKey(evaluateUrl: string): string {
  return `${prefix(evaluateUrl)}recent`;
}

/**
 * Records that the context whose values are stored under key (a storageKey
 * for evaluateUrl) was just used, and removes the values of the least
 * recently used contexts beyond maxStoredContexts, so that contexts with
 * volatile attributes cannot fill the storage the app shares. It returns a
 * promise when storage does.
 */
export function touch(storage: StorageAdapter, evaluateUrl: string, key: string): void | Promise<void> {
  const keys = prefix(evaluateUrl);
  const hash = key.slice(keys.length);
  const list = recentKey(evaluateUrl);
  const update = (raw: string | null | undefined): void | Promise<void> => {
    const recent = decodeRecent(raw);
    if (recent[0] === hash) return;
    const next = [hash, ...recent.filter((h) => h !== hash)];
    const pending: Promise<void>[] = [];
    // Remove first: that makes room for the list, and the values to come.
    for (const old of next.splice(maxStoredContexts)) {
      const done = storage.removeItem ? storage.removeItem(keys + old) : storage.setItem(keys + old, '');
      if (isPromise(done)) pending.push(done);
    }
    const done = storage.setItem(list, JSON.stringify(next));
    if (isPromise(done)) pending.push(done);
    if (pending.length > 0) return Promise.all(pending).then(() => {});
  };
  const raw = storage.getItem(list);
  return isPromise(raw) ? raw.then(update) : update(raw);
}

// decodeRecent returns the context hashes the list raw holds, skipping
// anything else: storage is shared, and a list another program wrote must
// not make a client remove that program's values.
function decodeRecent(raw: string | null | undefined): string[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw ?? '[]');
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  return parsed.filter((h): h is string => typeof h === 'string' && /^[0-9a-f]{40}$/.test(h));
}

// canonicalContext is context as JSON with its attributes sorted, so the same
// context written in another order shares a cache entry.
function canonicalContext(context: EvaluationContext): string {
  const attributes = Object.entries(context.attributes ?? {}).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  return JSON.stringify([context.key ?? null, attributes]);
}

function hex(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += b.toString(16).padStart(2, '0');
  return s;
}

/**
 * The flags raw holds (a stored evaluation, `{"flags": …}`, as the server
 * sends it), or undefined if raw is
 * missing or is not a stored evaluation: an entry another program wrote, or
 * one cut short.
 */
export function decodeFlags(raw: string | null | undefined): EvaluatedFlags | undefined {
  if (raw == null) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return undefined;
  }
  const flags = (parsed as { flags?: unknown } | null)?.flags;
  if (typeof flags !== 'object' || flags === null || Array.isArray(flags)) return undefined;
  for (const flag of Object.values(flags as Record<string, unknown>)) {
    const type = (flag as { type?: unknown } | null)?.type;
    if (!isFlagType(type) || !('value' in (flag as object))) return undefined;
  }
  return flags as EvaluatedFlags;
}

/** Reports whether value is a promise (or another thenable). */
export function isPromise<T>(value: T | Promise<T>): value is Promise<T> {
  return typeof (value as { then?: unknown } | null | undefined)?.then === 'function';
}
