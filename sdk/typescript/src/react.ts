import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
} from 'react';
import type { ReactNode } from 'react';
import type { BrowserClient, FlagSource } from './browser.js';
import type { EvaluatedFlags, EvaluationContext } from './types.js';

/** Props for a RealmProvider. */
export interface RealmProviderProps {
  /** The client the hooks below the provider read from. */
  client: BrowserClient;
  /**
   * Who flags are evaluated for. When it changes (by value, not by
   * reference), the client re-evaluates every flag for it. Leave it unset to
   * keep the client's own context.
   */
  context?: EvaluationContext;
  children?: ReactNode;
}

/** Whether a RealmProvider's client has its first evaluation. */
export interface RealmStatus {
  /**
   * True once the server's evaluation for the provider's context has
   * arrived. Until then, and again after the context changes, hooks return
   * bootstrap or stored values for that context, if the client has any, or
   * else their fallbacks; never another context's values.
   */
  ready: boolean;
  /**
   * Where the values hooks return came from: `bootstrap`, `cache` (stored
   * from an earlier evaluation for this context), or `network`; undefined
   * while hooks return their fallbacks.
   */
  source: FlagSource | undefined;
  /** Why the latest start failed, if it did and no evaluation has arrived since. */
  error: Error | undefined;
}

interface RealmContextValue {
  client: BrowserClient;
  // The provider's context as JSON, or undefined to follow the client's own.
  contextKey: string | undefined;
  status: RealmStatus;
}

// forContext reports whether client's values are for contextKey. They are
// not between a render with a new context and the effect that hands it to
// the client.
function forContext(client: BrowserClient, contextKey: string | undefined): boolean {
  return contextKey === undefined || JSON.stringify(client.context()) === contextKey;
}

const RealmContext = createContext<RealmContextValue | undefined>(undefined);

// What the provider's effect last applied to the client: its context, and
// how starting for it failed, if it did.
interface Applied {
  client: BrowserClient;
  contextKey: string | undefined;
  error: Error | undefined;
}

/**
 * Makes client available to the hooks in children. It starts the client when
 * it mounts and stops it when it unmounts; the last known values stay
 * available after that. When a start fails, the client keeps retrying, and
 * ready turns true once an evaluation arrives.
 */
export function RealmProvider({ client, context, children }: RealmProviderProps) {
  const [applied, setApplied] = useState<Applied | undefined>(undefined);
  // Compare contexts by value, so an inline object does not re-evaluate on every render.
  const contextKey = context === undefined ? undefined : JSON.stringify(context);

  useEffect(() => {
    let active = true;
    if (contextKey !== undefined) {
      // identify only sets the context of a stopped client; start then
      // evaluates for it, even if the client was started elsewhere.
      client.stop();
      void client.identify(JSON.parse(contextKey) as EvaluationContext);
    }
    setApplied({ client, contextKey, error: undefined });
    client.start().catch((e: unknown) => {
      if (!active) return;
      const error = e instanceof Error ? e : new Error(String(e));
      setApplied({ client, contextKey, error });
    });
    return () => {
      active = false;
      client.stop();
    };
  }, [client, contextKey]);

  const subscribe = useCallback((onChange: () => void) => client.subscribe(onChange), [client]);
  const clientReady = useSyncExternalStore(
    subscribe,
    () => client.isReady(),
    () => false,
  );
  const clientSource = useSyncExternalStore(
    subscribe,
    () => client.source(),
    () => undefined,
  );
  // Until the effect has handed the client this render's context, its
  // readiness is for another one.
  const current = applied !== undefined && applied.client === client && applied.contextKey === contextKey;
  const ready = current && clientReady;
  const error = current && !ready ? applied.error : undefined;
  const source = forContext(client, contextKey) ? clientSource : undefined;
  const value = useMemo(
    () => ({ client, contextKey, status: { ready, source, error } }),
    [client, contextKey, ready, source, error],
  );
  return createElement(RealmContext.Provider, { value }, children);
}

function useRealm(hook: string): RealmContextValue {
  const value = useContext(RealmContext);
  if (!value) throw new Error(`${hook} must be used within a RealmProvider`);
  return value;
}

// useClientValue reads from the provider's client and renders again whenever
// it reports a change. It reads fallback while the client's values are for
// another context than the provider's, and in server rendering, which has no
// evaluation.
function useClientValue<T>(hook: string, read: (client: BrowserClient) => T, fallback: T): T {
  const { client, contextKey } = useRealm(hook);
  const subscribe = useCallback((onChange: () => void) => client.subscribe(onChange), [client]);
  return useSyncExternalStore(
    subscribe,
    () => (forContext(client, contextKey) ? read(client) : fallback),
    () => fallback,
  );
}

/** The provider's client, for example to call identify. */
export function useRealmClient(): BrowserClient {
  return useRealm('useRealmClient').client;
}

/** Whether the provider's client has its first evaluation, where its values came from, or why starting failed. */
export function useRealmStatus(): RealmStatus {
  return useRealm('useRealmStatus').status;
}

/** The value of a boolean flag, or fallback. */
export function useBoolFlag(flag: string, fallback: boolean): boolean {
  return useClientValue('useBoolFlag', (c) => c.bool(flag, fallback), fallback);
}

/** The value of a string flag, or fallback. */
export function useStringFlag(flag: string, fallback: string): string {
  return useClientValue('useStringFlag', (c) => c.string(flag, fallback), fallback);
}

/** The value of a number flag, or fallback. */
export function useNumberFlag(flag: string, fallback: number): number {
  return useClientValue('useNumberFlag', (c) => c.number(flag, fallback), fallback);
}

/** The value of a custom (JSON) flag, or fallback. */
export function useJsonFlag<T>(flag: string, fallback: T): T {
  return useClientValue('useJsonFlag', (c) => c.json(flag, fallback), fallback);
}

const noFlags: EvaluatedFlags = {};

/** Every flag's current value. */
export function useAllFlags(): EvaluatedFlags {
  return useClientValue('useAllFlags', (c) => c.allFlags(), noFlags);
}
