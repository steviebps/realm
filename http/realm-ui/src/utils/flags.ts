// Type imports only, so node:test can run this file with Node's type stripping.
import type { Chamber, Flag, FlagType, FlagValue, TargetingRule, WireChamber } from '../models/response';

/** The initial value for a new flag or rule of the given type. */
export const defaultValueFor = (type: FlagType): FlagValue => {
  switch (type) {
    case 'boolean':
      return false;
    case 'number':
      return 0;
    case 'string':
      return '';
    case 'custom':
      return {};
  }
};

/**
 * Whether the flag's targeting is turned off: its rules are kept in `pausedTargeting`, which is never evaluated, so
 * every context gets the default. Rules in `targeting` win, as SDKs serve them.
 */
export const targetingPaused = (flag: Flag): boolean => !flag.targeting?.length && !!flag.pausedTargeting?.length;

/** The flag's targeting rules, whether targeting is on or off. */
export const rulesOf = (flag: Flag): TargetingRule[] =>
  (targetingPaused(flag) ? flag.pausedTargeting : flag.targeting) ?? [];

/**
 * An id for a new targeting rule of flag, such as `r-k3x9q2`, that none of its rules (paused or not) has. SDKs and the
 * evaluation endpoint report which rule served each value by its id (realm.flag.evaluations), and a rule without one
 * by its position, which changes when rules are reordered.
 */
export const newRuleId = (flag: Flag, random: () => number = Math.random): string => {
  const taken = new Set([...(flag.targeting ?? []), ...(flag.pausedTargeting ?? [])].map((rule) => rule.id));
  for (;;) {
    const id = `r-${Math.floor(random() * 36 ** 6)
      .toString(36)
      .padStart(6, '0')}`;
    if (!taken.has(id)) return id;
  }
};

/** Returns flag with the given fields set, or removed entirely when undefined. */
export const withFields = (flag: Flag, fields: Partial<Flag>): Flag =>
  Object.fromEntries(Object.entries({ ...flag, ...fields }).filter(([, v]) => v !== undefined)) as unknown as Flag;

/**
 * Returns chamber as the UI holds it (see Chamber): without the flags written as null, which serve nothing. Null when
 * there is no chamber.
 */
export const withoutNullFlags = (chamber: WireChamber | null | undefined): Chamber | null => {
  if (!chamber) return null;
  const rules = Object.entries(chamber.rules ?? {}).filter((entry): entry is [string, Flag] => entry[1] !== null);
  return { rules: Object.fromEntries(rules) };
};

/**
 * Returns flag with rules as its targeting rules: in `targeting`, or in `pausedTargeting` when paused. The other list
 * is removed, so a flag never has rules in both. A flag without rules has neither key.
 */
export const withRules = (flag: Flag, rules: TargetingRule[], paused: boolean): Flag =>
  withFields(flag, {
    targeting: rules.length && !paused ? rules : undefined,
    pausedTargeting: rules.length && paused ? rules : undefined,
  });

/**
 * Returns flag with its targeting turned off (its rules moved to `pausedTargeting`) or on (moved back to
 * `targeting`). Turning it off and on again leaves the flag as it was.
 */
export const withTargetingPaused = (flag: Flag, paused: boolean): Flag => withRules(flag, rulesOf(flag), paused);

/** Returns human-readable problems that would make the server reject the flag. */
export const validateFlag = (flag: Flag): string[] => {
  const problems: string[] = [];
  rulesOf(flag).forEach((rule, i) => {
    (rule.clauses ?? []).forEach((clause, j) => {
      const where = `Rule ${i + 1}, condition ${j + 1}`;
      if (!clause.attribute.trim()) problems.push(`${where}: choose an attribute.`);
      if (clause.values.length === 0) problems.push(`${where}: add at least one value.`);
    });
    const { start, end } = rule.schedule ?? {};
    if (rule.schedule && !start && !end) problems.push(`Rule ${i + 1}: set a start or an end for the schedule.`);
    if (start && end && Date.parse(end) <= Date.parse(start)) {
      problems.push(`Rule ${i + 1}: the schedule must end after it starts.`);
    }
  });
  return problems;
};

/**
 * Returns flag sealed or not. An unsealed flag has no `sealed` key, so toggling the seal off and on again leaves the
 * flag as it was.
 */
export const withSealed = (flag: Flag, sealed: boolean): Flag => withFields(flag, { sealed: sealed || undefined });
