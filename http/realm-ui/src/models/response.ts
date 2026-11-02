export type ListResponse = {
  data?: Array<string>;
  errors?: Array<string>;
};

export type ChamberResponse = {
  data?: Chamber;
  errors?: Array<string>;
};

export type Chamber = {
  rules: Rules;
};

export type Rules = Record<string, Flag>;

export type RuleType = 'string' | 'number' | 'boolean' | 'custom';

export type FlagValue = string | number | boolean | Record<string, unknown>;

export type Operator = 'in' | 'notIn' | 'startsWith' | 'semverGte' | 'semverLte';

export type Clause = {
  attribute: string;
  op: Operator;
  values: string[];
};

export type Rollout = {
  percentage: number;
  bucketBy?: string;
  seed?: string;
};

export type TargetingRule = {
  id?: string;
  description?: string;
  clauses?: Clause[];
  value: FlagValue;
  rollout?: Rollout;
};

export type Flag = {
  type: RuleType;
  value: FlagValue;
  targeting?: TargetingRule[];
};

export type Revision = {
  revision: number;
  time: string;
  actor: string;
  operation: 'put' | 'patch' | 'delete';
  etag?: string;
  chamber?: Chamber | null;
};

/** Attribute name → value → human-readable label. */
export type Catalog = Record<string, Record<string, string>>;
