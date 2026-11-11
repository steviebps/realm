// Type imports only, so node:test can run this file with Node's type stripping.
import type { Flag, FlagValue, RuleType } from '../models/response';

/** The initial value for a new flag or rule of the given type. */
export const defaultValueFor = (type: RuleType): FlagValue => {
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

/** Returns human-readable problems that would make the server reject the flag. */
export const validateFlag = (flag: Flag): string[] => {
  const problems: string[] = [];
  (flag.targeting ?? []).forEach((rule, i) => {
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
 * Returns flag sealed or not. The seal sits where the server writes it (after the default value), and an unsealed
 * flag has no `sealed` key, so toggling the seal off and on again leaves the flag as it was.
 */
export const withSealed = (flag: Flag, sealed: boolean): Flag => {
  const next: Flag = { type: flag.type, value: flag.value, ...(sealed ? { sealed: true } : {}) };
  // the other fields, in their order, after the seal
  for (const [key, v] of Object.entries(flag)) {
    if (key !== 'sealed' && !(key in next)) Object.assign(next, { [key]: v });
  }
  return next;
};
