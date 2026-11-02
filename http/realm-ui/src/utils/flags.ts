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
  });
  return problems;
};
