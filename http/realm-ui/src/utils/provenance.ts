// Type imports only: the node:test suite runs this file with Node's type
// stripping, which cannot resolve extensionless value imports.
import type { Flag, LayerStatus, Lineage } from '../models/response';

/** One chamber's definition of a flag. */
export type Definition = { path: string; flag: Flag };

/**
 * A flag served at a chamber, where it comes from, the definitions it overrides (farther than its source), and the
 * definitions its seal hides (nearer than its source, written before the seal), each nearest first.
 */
export type Served = {
  name: string;
  flag: Flag;
  source: string;
  overrides: Definition[];
  /** The farthest parent that seals the flag, when one does: the chamber cannot define it. */
  sealedBy?: string;
  hidden: Definition[];
};

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
      /** The flags it defines that a farther chamber seals, hiding its definitions, sorted by key. */
      hidden: { name: string; by: string }[];
    }
  | { kind: 'notInherited'; path: string; distance: number };

export type Provenance = {
  /** Whether the server applies inheritance at all. */
  inheritable: boolean;
  /** Flags served from the chamber itself. */
  own: Record<string, Served>;
  /** Flags served from parent chambers; null exactly when the inherited view cannot be built. */
  inherited: Record<string, Served> | null;
  /** Flag key → the farthest parent that seals it: flags the chamber cannot define (empty when the view is broken). */
  sealedBy: Record<string, string>;
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
  const sealedBy = broken ? {} : (lineage.sealedBy ?? {});
  const hiddenAt = broken ? {} : (lineage.hidden ?? {});
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
    const hidden = (get(hiddenAt, name) ?? []).flatMap((path) => {
      const at = indexOf.get(path);
      const definition = at === undefined ? undefined : get(layers[at].rules, name);
      return definition ? [{ path, flag: definition }] : [];
    });
    const served: Served = { name, flag, source, overrides, sealedBy: get(sealedBy, name), hidden };
    if (i === 0) own[name] = served;
    else inherited[name] = served;
  }

  /** Whether a seal hides the definition of key at path. */
  const hides = (key: string, path: string) => get(hiddenAt, key)?.includes(path) === true;

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
        return by !== undefined && by !== path && !hides(k, path) ? [{ name: k, by }] : [];
      }),
      hidden: keys.flatMap((k) => {
        const by = get(sealedBy, k);
        return by !== undefined && hides(k, path) ? [{ name: k, by }] : [];
      }),
    };
  });

  return { inheritable: lineage.inheritable, own, inherited: broken ? null : inherited, sealedBy, stack };
};
