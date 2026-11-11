import { Catalog, Chamber, Clause, Flag, FlagValue, Operator, Schedule, TargetingRule } from '../models/response';
import { formatTime } from './schedule';

export const OPERATOR_LABELS: Record<Operator, string> = {
  in: 'is one of',
  notIn: 'is not one of',
  startsWith: 'starts with',
  semverGte: 'is at least version',
  semverLte: 'is at most version',
};

/** Returns the catalog label for a value, falling back to the raw value. */
export const labelFor = (catalog: Catalog, attribute: string, value: string) => catalog[attribute]?.[value] ?? value;

export const formatValue = (value: FlagValue): string => JSON.stringify(value);

export const describeClause = (clause: Clause, catalog: Catalog) => {
  const values = clause.values.map((v) => labelFor(catalog, clause.attribute, v));
  return `${clause.attribute} ${OPERATOR_LABELS[clause.op]} ${values.join(', ')}`;
};

export const describeSchedule = ({ start, end }: Schedule) =>
  [start && `from ${formatTime(start)}`, end && `until ${formatTime(end)}`].filter(Boolean).join(' ');

/** A one-line, plain-language summary of a targeting rule. */
export const describeRule = (rule: TargetingRule, catalog: Catalog) => {
  const when = rule.clauses?.length ? rule.clauses.map((c) => describeClause(c, catalog)).join(' AND ') : 'everyone';
  let serve = `serve ${formatValue(rule.value)}`;
  if (rule.rollout) {
    serve += ` to ${rule.rollout.percentage}% (by ${rule.rollout.bucketBy || 'key'})`;
  }
  if (rule.schedule) {
    serve += `, ${describeSchedule(rule.schedule)}`;
  }
  return `If ${when} → ${serve}`;
};

export type FlagChange = {
  flag: string;
  kind: 'added' | 'removed' | 'changed';
  lines: { kind: 'add' | 'remove' | 'info'; text: string }[];
};

/** Whether two JSON values are equal; server JSON has a stable key order. */
export const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

const describeFlag = (flag: Flag, catalog: Catalog) => [
  `Default: ${formatValue(flag.value)}`,
  ...(flag.targeting ?? []).map((r, i) => `Rule ${i + 1}: ${describeRule(r, catalog)}`),
];

/** A flag's definition in plain language: what every context gets, or its default and each rule. */
export const describeDefinition = (flag: Flag, catalog: Catalog): string[] =>
  flag.targeting?.length
    ? describeFlag(flag, catalog)
    : [`No targeting: every context gets ${formatValue(flag.value)}.`];

/** How a change describes a flag's seal. */
const SEALED = "Sealed: chambers below this one can't override it";

/**
 * Computes a human-readable diff between two chambers, flag by flag, so an
 * editor can confirm exactly what will change before saving.
 */
export const diffChambers = (before: Chamber | null, after: Chamber | null, catalog: Catalog): FlagChange[] => {
  const a = before?.rules ?? {};
  const b = after?.rules ?? {};
  const names = Array.from(new Set([...Object.keys(a), ...Object.keys(b)])).sort();
  const changes: FlagChange[] = [];

  for (const name of names) {
    const was = a[name];
    const now = b[name];
    if (!was && now) {
      changes.push({
        flag: name,
        kind: 'added',
        lines: [
          { kind: 'info', text: `Type: ${now.type}` },
          ...(now.sealed ? [{ kind: 'info' as const, text: SEALED }] : []),
          ...describeFlag(now, catalog).map((text) => ({ kind: 'add' as const, text })),
        ],
      });
      continue;
    }
    if (was && !now) {
      changes.push({
        flag: name,
        kind: 'removed',
        lines: describeFlag(was, catalog).map((text) => ({ kind: 'remove', text })),
      });
      continue;
    }
    if (!was || !now || same(was, now)) {
      continue;
    }

    const lines: FlagChange['lines'] = [];
    if (Boolean(was.sealed) !== Boolean(now.sealed)) {
      lines.push({ kind: now.sealed ? 'add' : 'remove', text: SEALED });
    }
    if (!same(was.value, now.value)) {
      lines.push({ kind: 'remove', text: `Default: ${formatValue(was.value)}` });
      lines.push({ kind: 'add', text: `Default: ${formatValue(now.value)}` });
    }
    const wasRules = was.targeting ?? [];
    const nowRules = now.targeting ?? [];
    if (!same(wasRules, nowRules)) {
      wasRules.forEach((r, i) => {
        if (!same(r, nowRules[i])) lines.push({ kind: 'remove', text: `Rule ${i + 1}: ${describeRule(r, catalog)}` });
      });
      nowRules.forEach((r, i) => {
        if (!same(r, wasRules[i])) lines.push({ kind: 'add', text: `Rule ${i + 1}: ${describeRule(r, catalog)}` });
      });
    }
    changes.push({ flag: name, kind: 'changed', lines });
  }
  return changes;
};
