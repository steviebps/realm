// Type imports only from models, and .ts extensions, so node:test can run this file with Node's type stripping.
import type { Catalog, Chamber, Clause, Flag, FlagValue, Operator, Schedule, TargetingRule } from '../models/response';
import { jsonEqual, summarizeConstraints } from './constraints.ts';
import { rulesOf, targetingPaused } from './flags.ts';
import type { NullFlag } from './provenance.ts';
import { formatTime } from './schedule.ts';

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
    const seed = rule.rollout.seed ? `, seed ${rule.rollout.seed}` : '';
    serve += ` to ${rule.rollout.percentage}% (by ${rule.rollout.bucketBy || 'key'}${seed})`;
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

/** Whether two JSON values are equal, whatever the order of their members. */
export const same = jsonEqual;

/** How a change describes a flag whose targeting is turned off. */
const TARGETING_OFF = 'Targeting off: every context gets the default, and the rules are kept';

/** A rule's line: paused rules say so, and a rule's id and description are part of it, so changing them shows. */
const ruleLine = (rule: TargetingRule, i: number, paused: boolean, catalog: Catalog) => {
  const id = rule.id ? ` (${rule.id})` : '';
  const description = rule.description ? ` — ${rule.description}` : '';
  return `${paused ? 'Paused rule' : 'Rule'} ${i + 1}${id}: ${describeRule(rule, catalog)}${description}`;
};

const describeRules = (flag: Flag, catalog: Catalog) =>
  rulesOf(flag).map((r, i) => ruleLine(r, i, targetingPaused(flag), catalog));

const describeFlag = (flag: Flag, catalog: Catalog) => [
  `Default: ${formatValue(flag.value)}`,
  ...(targetingPaused(flag) ? [TARGETING_OFF] : []),
  ...describeRules(flag, catalog),
];

/** A flag's definition in plain language: what every context gets, or its default and each rule. */
export const describeDefinition = (flag: Flag, catalog: Catalog): string[] => {
  if (targetingPaused(flag)) {
    return [`Targeting off: every context gets ${formatValue(flag.value)}.`, ...describeRules(flag, catalog)];
  }
  return flag.targeting?.length
    ? describeFlag(flag, catalog)
    : [`No targeting: every context gets ${formatValue(flag.value)}.`];
};

/** How a change describes a flag's seal. */
const SEALED = "Sealed: chambers below this one can't override it";

/** How a change describes each thing a flag says about itself, when it says it. */
const ABOUT = {
  description: (f: Flag) => f.description !== undefined && `Description: ${f.description}`,
  owner: (f: Flag) => f.owner !== undefined && `Owner: ${f.owner}`,
  lifecycle: (f: Flag) => f.lifecycle !== undefined && `Lifecycle: ${f.lifecycle}`,
  constraints: (f: Flag) =>
    f.constraints !== undefined && `Allowed values: ${summarizeConstraints(f.constraints, f.type)}`,
};

/** How a change describes removing a flag the stored chamber writes as null: what the chamber serves instead. */
const nullRemoved = ({ inherits, unknown }: NullFlag, catalog: Catalog): FlagChange['lines'] => {
  if (inherits) {
    return [
      { kind: 'remove', text: `Written as null: serves nothing here, hiding ${inherits.path}'s definition` },
      { kind: 'info', text: `Saving removes it, so this chamber serves ${inherits.path}'s definition:` },
      ...describeDefinition(inherits.flag, catalog).map((text) => ({ kind: 'add' as const, text })),
    ];
  }
  return [
    { kind: 'remove', text: 'Written as null' },
    unknown
      ? { kind: 'info', text: "Saving removes it, so this chamber serves a parent chamber's definition, if one has it" }
      : { kind: 'info', text: 'Saving removes it; what this chamber serves for it does not change' },
  ];
};

/**
 * Computes a human-readable diff between two chambers, flag by flag, so an
 * editor can confirm exactly what will change before saving.
 */
export const diffChambers = (
  before: Chamber | null,
  after: Chamber | null,
  catalog: Catalog,
  /** The flags the stored chamber writes as null, which before leaves out: saving after removes them. */
  nullFlags: NullFlag[] = []
): FlagChange[] => {
  const a = before?.rules ?? {};
  const b = after?.rules ?? {};
  const nulls = new Map(nullFlags.map((n) => [n.name, n]));
  const names = Array.from(new Set([...Object.keys(a), ...Object.keys(b), ...nulls.keys()])).sort();
  const changes: FlagChange[] = [];

  for (const name of names) {
    const was = a[name];
    const now = b[name];
    const nulled = nulls.get(name);
    if (!was && !now && nulled) {
      changes.push({ flag: name, kind: 'removed', lines: nullRemoved(nulled, catalog) });
      continue;
    }
    if (!was && now) {
      changes.push({
        flag: name,
        kind: 'added',
        lines: [
          { kind: 'info', text: `Type: ${now.type}` },
          ...Object.values(ABOUT).flatMap((line) => {
            const text = line(now);
            return text ? [{ kind: 'info' as const, text }] : [];
          }),
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
    if (was.type !== now.type) {
      lines.push({ kind: 'remove', text: `Type: ${was.type}` });
      lines.push({ kind: 'add', text: `Type: ${now.type}` });
    }
    for (const [k, line] of Object.entries(ABOUT) as [keyof typeof ABOUT, (f: Flag) => string | false][]) {
      if (same(was[k], now[k])) continue;
      let before = line(was);
      let after = line(now);
      // constraints whose summaries read the same differ in what the summary leaves out: show them whole
      if (before && before === after) {
        before = `Allowed values: ${JSON.stringify(was[k])}`;
        after = `Allowed values: ${JSON.stringify(now[k])}`;
      }
      if (before) lines.push({ kind: 'remove', text: before });
      if (after) lines.push({ kind: 'add', text: after });
    }
    if (Boolean(was.sealed) !== Boolean(now.sealed)) {
      lines.push({ kind: now.sealed ? 'add' : 'remove', text: SEALED });
    }
    if (!same(was.value, now.value)) {
      lines.push({ kind: 'remove', text: `Default: ${formatValue(was.value)}` });
      lines.push({ kind: 'add', text: `Default: ${formatValue(now.value)}` });
    }
    // turning targeting off or on moves the rules between lists; only rules that change are listed
    const wasPaused = targetingPaused(was);
    const nowPaused = targetingPaused(now);
    if (wasPaused !== nowPaused) {
      lines.push({ kind: nowPaused ? 'add' : 'remove', text: TARGETING_OFF });
    }
    const wasRules = rulesOf(was);
    const nowRules = rulesOf(now);
    if (!same(wasRules, nowRules)) {
      wasRules.forEach((r, i) => {
        if (!same(r, nowRules[i])) lines.push({ kind: 'remove', text: ruleLine(r, i, wasPaused, catalog) });
      });
      nowRules.forEach((r, i) => {
        if (!same(r, wasRules[i])) lines.push({ kind: 'add', text: ruleLine(r, i, nowPaused, catalog) });
      });
    }
    changes.push({ flag: name, kind: 'changed', lines });
  }
  return changes;
};
