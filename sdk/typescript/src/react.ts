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
import { contextKeyOf, readFlag, type BrowserClient, type FlagSource } from './browser.js';
import { copyValue } from './evaluate.js';
import type { EvaluatedFlags, EvaluationContext, FlagType } from './types.js';

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
   * arrived, or while hooks return a server render's evaluation for it (the
   * client's initialState), including in the server render itself. Until
   * then, and again after the context changes, hooks return bootstrap or
   * stored values for that context, if the client has any, or else their
   * fallbacks; never another context's values.
   */
  ready: boolean;
  /**
   * Where the values hooks return came from: `render` (the client's
   * initialState), `bootstrap`, `cache` (stored from an earlier evaluation
   * for this context), or `network`; undefined while hooks return their
   * fallbacks.
   */
  source: FlagSource | undefined;
  /** Why the latest start failed, if it did and no evaluation has arrived since. */
  error: Error | undefined;
}

interface RealmContextValue {
  client: BrowserClient;
  // The provider's context as JSON, or undefined to follow the client's own.
  contextKey: string | undefined;
  // The values a server render reads (see BrowserClient.renderedValues).
  rendered: ReturnType<BrowserClient['renderedValues']>;
  status: RealmStatus;
}

// forContext reports whether client's values are for contextKey. They are
// not between a render with a new context and the effect that hands it to
// the client.
function forContext(client: BrowserClient, contextKey: string | undefined): boolean {
  return contextKey === undefined || client.contextKey() === contextKey;
}

const RealmContext = createContext<RealmContextValue | undefined>(undefined);

// How the mounted providers use a client: how many use it, and whether they
// started it (it was stopped when the first of them mounted). Providers stop
// a client only when they started it and the last of them unmounts, so a
// client the application started itself keeps running, and providers that
// share a client do not stop it for each other.
interface ProviderUse {
  count: number;
  started: boolean;
}

const providerUses = new WeakMap<BrowserClient, ProviderUse>();

// What the provider's effect last applied to the client: its context, and
// how starting for it failed, if it did.
interface Applied {
  client: BrowserClient;
  contextKey: string | undefined;
  error: Error | undefined;
}

/**
 * Makes client available to the hooks in children. It starts the client when
 * it mounts, and stops it when it unmounts if providers started it and no
 * other provider still uses it: a client the application started itself
 * keeps running. The last known values stay available after it stops. When a
 * start fails, the client keeps retrying, and ready turns true once an
 * evaluation arrives. A context that differs only in the order of its
 * attributes, or numbers written for strings (see contextKeyOf), is the same
 * context, and is not evaluated again.
 *
 * In server rendering, which never starts the client, and while the page
 * hydrates, hooks return the values the client was constructed with for the
 * provider's context: its initialState, else its bootstrap values, else
 * their fallbacks. Construct the client with the same initialState (and
 * bootstrap) on the server and in the browser, so the hydrated page matches
 * the server's; after hydration, hooks read the client's current values.
 */
export function RealmProvider({ client, context, children }: RealmProviderProps) {
  const [applied, setApplied] = useState<Applied | undefined>(undefined);
  // Compare contexts as the server receives them (see contextKeyOf), so an
  // inline object, or one with its attributes in another order, does not
  // evaluate again.
  const contextKey = context === undefined ? undefined : contextKeyOf(context);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const keyed = useMemo(() => context, [contextKey]);

  useEffect(() => {
    let active = true;
    let use = providerUses.get(client);
    if (!use || use.count === 0) {
      use = { count: 0, started: typeof client.isStarted === 'function' ? !client.isStarted() : true };
      providerUses.set(client, use);
    }
    use.count++;
    // identify re-evaluates a started client for a new context, and only
    // sets it on a stopped one, which start then evaluates for
    if (keyed !== undefined) void client.identify(keyed);
    setApplied({ client, contextKey, error: undefined });
    client.start().catch((e: unknown) => {
      if (!active) return;
      const error = e instanceof Error ? e : new Error(String(e));
      setApplied({ client, contextKey, error });
    });
    return () => {
      active = false;
      use.count--;
      if (use.count === 0 && use.started) client.stop();
    };
  }, [client, contextKey, keyed]);

  // By value, like contextKey.
  // A client this module did not construct (another copy of the SDK, a
  // test double) may lack it, and renders its fallbacks.
  const rendered = useMemo(
    () => (typeof client.renderedValues === 'function' ? client.renderedValues(keyed) : undefined),
    [client, keyed],
  );
  const subscribe = useCallback((onChange: () => void) => client.subscribe(onChange), [client]);
  const source = useSyncExternalStore(
    subscribe,
    () => (forContext(client, contextKey) ? client.source() : undefined),
    () => rendered?.source,
  );
  // Until the effect has handed the client this render's context, its
  // readiness is for another one; a server render's values are for the
  // context they were read for.
  const current = applied !== undefined && applied.client === client && applied.contextKey === contextKey;
  const ready = source === 'render' || (current && source === 'network');
  // Render values are ready, but a start that failed still says why.
  const error = current && source !== 'network' ? applied.error : undefined;
  const value = useMemo(
    () => ({ client, contextKey, rendered, status: { ready, source, error } }),
    [client, contextKey, rendered, ready, source, error],
  );
  return createElement(RealmContext.Provider, { value }, children);
}

function useRealm(hook: string): RealmContextValue {
  const value = useContext(RealmContext);
  if (!value) throw new Error(`${hook} must be used within a RealmProvider`);
  return value;
}

// useClientValue reads from the provider's client's values and renders again
// whenever it reports a change. It reads fallback while the client's values
// are for another context than the provider's. In server rendering and
// hydration, it reads the values the client was constructed with for the
// provider's context (see renderedValues), or else fallback.
function useClientValue<T>(hook: string, read: (flags: EvaluatedFlags) => T, fallback: T): T {
  const { client, contextKey, rendered } = useRealm(hook);
  const subscribe = useCallback((onChange: () => void) => client.subscribe(onChange), [client]);
  return useSyncExternalStore(
    subscribe,
    () => (forContext(client, contextKey) ? read(client.allFlags()) : fallback),
    () => (rendered ? read(rendered.flags) : fallback),
  );
}

// useTypedFlag reads flag when it has type want, or else fallback.
function useTypedFlag<T>(hook: string, flag: string, want: FlagType, fallback: T): T {
  return useClientValue(hook, (flags) => readFlag(flags, flag, want, fallback), fallback);
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
  return useTypedFlag('useBoolFlag', flag, 'boolean', fallback);
}

/** The value of a string flag, or fallback. */
export function useStringFlag(flag: string, fallback: string): string {
  return useTypedFlag('useStringFlag', flag, 'string', fallback);
}

/** The value of a number flag, or fallback. */
export function useNumberFlag(flag: string, fallback: number): number {
  return useTypedFlag('useNumberFlag', flag, 'number', fallback);
}

// absent stands for a custom flag's missing value in useJsonFlag.
const absent: unique symbol = Symbol('absent');

/**
 * The value of a custom (JSON) flag, or fallback. The value is this
 * component's copy, made again only when the flag changes: changing it
 * changes neither the client's value nor what other components read.
 */
export function useJsonFlag<T>(flag: string, fallback: T): T {
  // The client's own value, which stays the same object until it changes,
  // as useSyncExternalStore requires; json would copy it on every call.
  const shared = useClientValue(
    'useJsonFlag',
    (flags) => {
      const f = Object.hasOwn(flags, flag) ? flags[flag] : undefined;
      return f?.type === 'custom' ? f.value : absent;
    },
    absent,
  );
  const value = useMemo(() => (shared === absent ? absent : copyValue(shared)), [shared]);
  return value === absent ? fallback : (value as T);
}

const noFlags: EvaluatedFlags = {};

/**
 * Every flag's current value. The object is the client's own (see
 * BrowserClient.allFlags), so treat it as read only.
 */
export function useAllFlags(): EvaluatedFlags {
  return useClientValue('useAllFlags', (flags) => flags, noFlags);
}
