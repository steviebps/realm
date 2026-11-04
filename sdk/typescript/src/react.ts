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
import type { BrowserClient } from './browser.js';
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
   * True once the evaluation for the provider's context has arrived. Until
   * then, and again after the context changes, hooks return their fallbacks
   * (or, briefly, the previous context's values).
   */
  ready: boolean;
  /** Why the latest start failed, if it did and no evaluation has arrived since. */
  error: Error | undefined;
}

interface RealmContextValue {
  client: BrowserClient;
  status: RealmStatus;
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
  // Until the effect has handed the client this render's context, its
  // readiness is for another one.
  const current = applied !== undefined && applied.client === client && applied.contextKey === contextKey;
  const ready = current && clientReady;
  const error = current && !ready ? applied.error : undefined;
  const value = useMemo(() => ({ client, status: { ready, error } }), [client, ready, error]);
  return createElement(RealmContext.Provider, { value }, children);
}

function useRealm(hook: string): RealmContextValue {
  const value = useContext(RealmContext);
  if (!value) throw new Error(`${hook} must be used within a RealmProvider`);
  return value;
}

// useClientValue reads from the provider's client and renders again whenever
// it reports a change. Server rendering has no evaluation, so it reads fallback.
function useClientValue<T>(hook: string, read: (client: BrowserClient) => T, fallback: T): T {
  const { client } = useRealm(hook);
  const subscribe = useCallback((onChange: () => void) => client.subscribe(onChange), [client]);
  return useSyncExternalStore(
    subscribe,
    () => read(client),
    () => fallback,
  );
}

/** The provider's client, for example to call identify. */
export function useRealmClient(): BrowserClient {
  return useRealm('useRealmClient').client;
}

/** Whether the provider's client has its first evaluation, or why it failed. */
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
