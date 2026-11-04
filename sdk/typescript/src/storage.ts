import { sha1 } from './sha1.js';
import type { EvaluatedFlags, EvaluationContext, FlagType } from './types.js';

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
  };
}

/**
 * The key a client's values for context are stored under. It names the
 * evaluate URL (server and chamber) and a hash of the context, so values for
 * one user are never read back for another, and the context's attributes are
 * not written in the clear.
 */
export function storageKey(evaluateUrl: string, context: EvaluationContext): string {
  return `realm:v1:${evaluateUrl}:${hex(sha1(new TextEncoder().encode(canonicalContext(context))))}`;
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

/** The stored form of a client's values. */
export function encodeFlags(flags: EvaluatedFlags): string {
  return JSON.stringify({ flags });
}

const flagTypes = new Set<FlagType>(['string', 'number', 'boolean', 'custom']);

/**
 * The flags raw (as encodeFlags wrote it) holds, or undefined if raw is
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
    if (!flagTypes.has(type as FlagType) || !('value' in (flag as object))) return undefined;
  }
  return flags as EvaluatedFlags;
}
