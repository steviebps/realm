import { Flag, FlagValue, RuleType } from '../models/response';

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
