// A port of realm's evaluation (pkg/targeting.go and pkg/flag.go). It must
// produce exactly what the Go SDK and server do; sdk/conformance/evaluation.json
// holds the cases both run.

import { canonical, compareParsed, parse } from './semver.js';
import { sha1Words } from './sha1.js';
import type { Chamber, Clause, EvaluatedFlags, EvaluationContext, Flag, FlagType, Rollout, Schedule } from './types.js';

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
  const [h0, h1] = sha1Words(encoder.encode(`${seed}.${key}`));
  // Both terms are exact; adding them rounds the 60-bit value to the nearest
  // double, exactly as Go's float64(uint64) conversion does.
  const val = h0! * 2 ** 28 + (h1! >>> 4);
  return (val / LONG_SCALE) * 100;
}

/**
 * Returns the named attribute of context, falling back to the process-wide
 * defaults, or undefined when it is absent. `key` resolves to context.key.
 * Values are read as attributeValue reads them, so a number that a
 * JavaScript caller passed matches as its string.
 */
export function attribute(
  context: EvaluationContext,
  defaults: Record<string, string> | undefined,
  name: string,
): string | undefined {
  if (name === KEY_ATTRIBUTE) {
    const key = attributeValue(context.key);
    if (key) return key;
  }
  const attributes = context.attributes;
  if (typeof attributes === 'object' && attributes !== null && Object.hasOwn(attributes, name)) {
    const value = attributeValue(attributes[name]);
    if (value !== undefined) return value;
  }
  if (defaults && Object.hasOwn(defaults, name)) return attributeValue(defaults[name]);
  return undefined;
}

/**
 * Returns value as an attribute value: a string as it is, and a number or
 * boolean (which JavaScript callers pass, but the server does not accept) as
 * a string. Anything else is undefined, so it counts as a missing attribute.
 */
export function attributeValue(value: unknown): string | undefined {
  switch (typeof value) {
    case 'string':
      return value;
    case 'number':
    case 'boolean':
      return String(value);
    default:
      return undefined;
  }
}

/**
 * Returns attributes with every value a string, as attributeValue reads it,
 * leaving out values it cannot read; undefined when attributes is not an
 * object. It returns attributes itself when every value is a string already.
 */
export function stringAttributes(attributes: unknown): Record<string, string> | undefined {
  if (typeof attributes !== 'object' || attributes === null || Array.isArray(attributes)) return undefined;
  const entries = Object.entries(attributes);
  if (entries.every(([, value]) => typeof value === 'string')) return attributes as Record<string, string>;
  const strings: Record<string, string> = {};
  for (const [name, value] of entries) {
    const v = attributeValue(value);
    if (v !== undefined) strings[name] = v;
  }
  return strings;
}

/**
 * Returns context as the server accepts it: its key and attribute values as
 * strings (see attributeValue), leaving out values that cannot be. It returns
 * context itself when that changes nothing, and an empty context for one
 * that is not an object.
 */
export function normalizeContext(context: EvaluationContext | null | undefined): EvaluationContext {
  if (typeof context !== 'object' || context === null) return {};
  const key = context.key === undefined ? undefined : attributeValue(context.key);
  const attributes = context.attributes === undefined ? undefined : stringAttributes(context.attributes);
  if (key === context.key && attributes === context.attributes) return context;
  const normalized: EvaluationContext = {};
  if (key !== undefined) normalized.key = key;
  if (attributes !== undefined) normalized.attributes = attributes;
  return normalized;
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
      const have = parse(canonical(v));
      if (!have) return false;
      const bound = parse(canonical(clause.values[0] ?? ''));
      // a valid version is greater than an invalid bound, as in Go
      const cmp = bound ? compareParsed(have, bound) : 1;
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

// Parsed schedule bounds in milliseconds, so evaluation parses each schedule
// once rather than on every call. Chambers are not mutated once received.
const scheduleBounds = new WeakMap<Schedule, readonly [number, number]>();

/**
 * Reports whether now (milliseconds since the epoch) falls within the
 * schedule: at or after its start and before its end. No schedule includes
 * every time.
 */
export function scheduleIncludes(schedule: Schedule | undefined, now: number): boolean {
  if (!schedule) return true;
  let bounds = scheduleBounds.get(schedule);
  if (!bounds) {
    bounds = [
      schedule.start === undefined ? -Infinity : Date.parse(schedule.start),
      schedule.end === undefined ? Infinity : Date.parse(schedule.end),
    ];
    scheduleBounds.set(schedule, bounds);
  }
  return now >= bounds[0] && now < bounds[1];
}

/**
 * Returns the value of the first targeting rule that applies, or the flag's
 * default. Schedules are evaluated at now, which defaults to the current time.
 */
export function flagValue(
  flagKey: string,
  flag: Flag,
  context: EvaluationContext,
  defaults?: Record<string, string>,
  now: number = Date.now(),
): unknown {
  for (const rule of flag.targeting ?? []) {
    if (!scheduleIncludes(rule.schedule, now)) continue;
    const matches = (rule.clauses ?? []).every((c) => clauseMatches(c, context, defaults));
    if (matches && rolloutIncludes(rule.rollout, flagKey, context, defaults)) return rule.value;
  }
  return flag.value;
}

/**
 * Resolves every flag in the chamber for context, evaluating every schedule at
 * the same instant, now (the current time by default). A flag of a type this
 * SDK does not know (from a newer server) is left out, as the Go SDK leaves
 * it out, and so is one too malformed to evaluate, rather than failing every
 * flag; getters return their fallbacks for both.
 */
export function evaluateChamber(
  chamber: Chamber,
  context: EvaluationContext,
  defaults?: Record<string, string>,
  now: number = Date.now(),
): EvaluatedFlags {
  const flags: EvaluatedFlags = {};
  for (const [key, flag] of Object.entries(chamber.rules ?? {})) {
    if (!flag || !isFlagType(flag.type)) continue;
    try {
      flags[key] = { type: flag.type, value: flagValue(key, flag, context, defaults, now) };
    } catch {
      // malformed: left out
    }
  }
  return flags;
}

const flagTypes: ReadonlySet<unknown> = new Set<FlagType>(['string', 'number', 'boolean', 'custom']);

/** Reports whether type is one of the flag types this SDK knows. */
export function isFlagType(type: unknown): type is FlagType {
  return flagTypes.has(type);
}

/** Returns value when the flag has the wanted type, otherwise fallback. */
export function typedValue<T>(type: FlagType | undefined, value: unknown, want: FlagType, fallback: T): T {
  if (type !== want || (want !== 'custom' && typeof value !== want)) return fallback;
  return value as T;
}

/**
 * Returns a deep copy of value, a flag's value, so that a caller that changes
 * the copy does not change what later reads return. Primitives are returned
 * as they are, and so is a value that cannot be copied: one from the server
 * is JSON, which always can, but a bootstrap value might not be.
 */
export function copyValue<T>(value: T): T {
  if (typeof value !== 'object' || value === null) return value;
  try {
    return typeof structuredClone === 'function' ? structuredClone(value) : (JSON.parse(JSON.stringify(value)) as T);
  } catch {
    return value;
  }
}
