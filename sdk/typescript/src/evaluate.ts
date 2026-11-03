// A port of realm's evaluation (pkg/targeting.go and pkg/flag.go). It must
// produce exactly what the Go SDK and server do; sdk/conformance/evaluation.json
// holds the cases both run.

import { canonical, compare, isValid } from './semver.js';
import { sha1 } from './sha1.js';
import type { Chamber, Clause, EvaluatedFlags, EvaluationContext, Flag, FlagType, Rollout } from './types.js';

/** The attribute name under which `EvaluationContext.key` is exposed. */
export const KEY_ATTRIBUTE = 'key';

/** The attribute the `version` option sets, matched by the semver operators by convention. */
export const VERSION_ATTRIBUTE = 'version';

// The maximum value of the 15 leading hex digits (60 bits) of the bucketing hash.
const LONG_SCALE = 0xfffffffffffffff;

const encoder = new TextEncoder();

/**
 * Returns a stable value in [0, 100) for seed and key: the 60 leading bits of
 * SHA-1("<seed>.<key>"), scaled.
 */
export function bucket(seed: string, key: string): number {
  const sum = sha1(encoder.encode(`${seed}.${key}`));
  let val = 0n;
  for (let i = 0; i < 8; i++) val = (val << 8n) | BigInt(sum[i]!);
  return (Number(val >> 4n) / LONG_SCALE) * 100;
}

/**
 * Returns the named attribute of context, falling back to the process-wide
 * defaults, or undefined when it is absent. `key` resolves to context.key.
 */
export function attribute(
  context: EvaluationContext,
  defaults: Record<string, string> | undefined,
  name: string,
): string | undefined {
  if (name === KEY_ATTRIBUTE && context.key) return context.key;
  if (context.attributes && Object.hasOwn(context.attributes, name)) return context.attributes[name];
  if (defaults && Object.hasOwn(defaults, name)) return defaults[name];
  return undefined;
}

/** Reports whether the clause holds for context. A missing attribute never matches. */
export function clauseMatches(
  clause: Clause,
  context: EvaluationContext,
  defaults?: Record<string, string>,
): boolean {
  const v = attribute(context, defaults, clause.attribute);
  if (v === undefined) return false;
  switch (clause.op) {
    case 'in':
      return clause.values.includes(v);
    case 'notIn':
      return !clause.values.includes(v);
    case 'startsWith':
      return clause.values.some((want) => v.startsWith(want));
    case 'semverGte':
    case 'semverLte': {
      const have = canonical(v);
      if (!isValid(have)) return false;
      const cmp = compare(have, canonical(clause.values[0] ?? ''));
      return clause.op === 'semverGte' ? cmp >= 0 : cmp <= 0;
    }
    default:
      return false;
  }
}

/**
 * Reports whether context falls within the rollout. No rollout includes every
 * context; a context missing the bucketing attribute is never included.
 */
export function rolloutIncludes(
  rollout: Rollout | undefined,
  flagKey: string,
  context: EvaluationContext,
  defaults?: Record<string, string>,
): boolean {
  if (!rollout) return true;
  const v = attribute(context, defaults, rollout.bucketBy || KEY_ATTRIBUTE);
  if (!v) return false;
  return bucket(rollout.seed || flagKey, v) < rollout.percentage;
}

/** Returns the value of the first targeting rule that applies, or the flag's default. */
export function flagValue(
  flagKey: string,
  flag: Flag,
  context: EvaluationContext,
  defaults?: Record<string, string>,
): unknown {
  for (const rule of flag.targeting ?? []) {
    const matches = (rule.clauses ?? []).every((c) => clauseMatches(c, context, defaults));
    if (matches && rolloutIncludes(rule.rollout, flagKey, context, defaults)) return rule.value;
  }
  return flag.value;
}

/** Resolves every flag in the chamber for context. */
export function evaluateChamber(
  chamber: Chamber,
  context: EvaluationContext,
  defaults?: Record<string, string>,
): EvaluatedFlags {
  const flags: EvaluatedFlags = {};
  for (const [key, flag] of Object.entries(chamber.rules ?? {})) {
    if (flag) flags[key] = { type: flag.type, value: flagValue(key, flag, context, defaults) };
  }
  return flags;
}

const jsTypes: Record<FlagType, string | undefined> = {
  string: 'string',
  number: 'number',
  boolean: 'boolean',
  custom: undefined,
};

/** Returns value when the flag has the wanted type, otherwise fallback. */
export function typedValue<T>(type: FlagType | undefined, value: unknown, want: FlagType, fallback: T): T {
  if (type !== want) return fallback;
  const js = jsTypes[want];
  if (js !== undefined && typeof value !== js) return fallback;
  return value as T;
}
