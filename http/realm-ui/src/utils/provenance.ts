// Type imports only: the node:test suite runs this file with Node's type
// stripping, which cannot resolve extensionless value imports.
import type { Flag, LayerStatus, Lineage } from '../models/response';

/** One chamber's definition of a flag. */
export type Definition = { path: string; flag: Flag };

/** A flag served at a chamber, where it comes from, and the definitions it overrides, nearest first. */
export type Served = { name: string; flag: Flag; source: string; overrides: Definition[] };

/** An ancestor of a chamber, nearest first: a parent chamber the server consults, or one it does not. */
export type StackEntry =
  | {
      kind: 'layer';
      path: string;
      /** How many levels up: 1 for the parent. */
      distance: number;
      status: LayerStatus;
      error?: string;
      /** How many flags it defines. */
      defined: number;
      /** The flags it serves here, sorted by key. */
      served: Served[];
      /** The flags it defines that a nearer chamber overrides, sorted by key. */
      overridden: { name: string; by: string }[];
    }
  | { kind: 'notInherited'; path: string; distance: number };

export type Provenance = {
  /** Whether the server applies inheritance at all. */
  inheritable: boolean;
  /** Flags served from the chamber itself. */
  own: Record<string, Served>;
  /** Flags served from parent chambers; null exactly when the inherited view cannot be built. */
  inherited: Record<string, Served> | null;
  /** Every ancestor of the chamber, nearest first. */
  stack: StackEntry[];
};

/** record[key] when record has its own key: a flag named "constructor" must not find Object.prototype's. */
export const get = <T>(record: Record<string, T> | null | undefined, key: string): T | undefined =>
  record && Object.prototype.hasOwnProperty.call(record, key) ? record[key] : undefined;

/**
 * Returns the ancestors of a chamber path, nearest first: "/a/b/c/" →
 * ["/a/b/", "/a/", "/"]; "/" → []. Pure string work: it says nothing about
 * which of them are inherited.
 */
export const ancestorsOf = (path: string): string[] => {
  const segments = path.split('/').filter(Boolean);
  const ancestors: string[] = [];
  for (let n = segments.length - 1; n >= 0; n--) {
    ancestors.push(n === 0 ? '/' : `/${segments.slice(0, n).join('/')}/`);
  }
  return ancestors;
};

/**
 * Groups the server's lineage for display. It decides nothing: the server's
 * `layers` say which chambers are consulted and `sources` says which
 * definition of each flag is served.
 */
export const provenance = (lineage: Lineage): Provenance => {
  const { layers } = lineage;
  const broken = lineage.error !== undefined;
  // a broken view attributes nothing
  const sources = broken ? {} : (lineage.sources ?? {});
  const indexOf = new Map(layers.map((l, i) => [l.path, i]));

  const own: Record<string, Served> = {};
  const inherited: Record<string, Served> = {};
  for (const [name, source] of Object.entries(sources)) {
    const i = indexOf.get(source);
    const flag = i === undefined ? undefined : get(layers[i].rules, name);
    if (i === undefined || !flag) continue;
    const overrides = layers.slice(i + 1).flatMap((l) => {
      const definition = get(l.rules, name);
      return definition ? [{ path: l.path, flag: definition }] : [];
    });
    const served = { name, flag, source, overrides };
    if (i === 0) own[name] = served;
    else inherited[name] = served;
  }

  // ancestorsOf is nearest first, so the index says how many levels up
  const stack = ancestorsOf(layers[0].path).map((path, i): StackEntry => {
    const distance = i + 1;
    const at = indexOf.get(path);
    const layer = at === undefined ? undefined : layers[at];
    if (!layer) return { kind: 'notInherited', path, distance };
    const keys = Object.keys(layer.rules ?? {}).sort();
    return {
      kind: 'layer',
      path,
      distance,
      status: layer.status,
      error: layer.error,
      defined: keys.length,
      served: keys.flatMap((k) => {
        const served = get(inherited, k);
        return served?.source === path ? [served] : [];
      }),
      overridden: keys.flatMap((k) => {
        const by = get(sources, k);
        return by !== undefined && by !== path ? [{ name: k, by }] : [];
      }),
    };
  });

  return { inheritable: lineage.inheritable, own, inherited: broken ? null : inherited, stack };
};
