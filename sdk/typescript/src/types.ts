/** The type of a flag's value. `custom` values are arbitrary JSON. */
export type FlagType = 'string' | 'number' | 'boolean' | 'custom';

/** The comparison a clause applies between a context attribute and its values. */
export type Operator = 'in' | 'notIn' | 'startsWith' | 'semverGte' | 'semverLte';

/** A single condition of a targeting rule, matched against one attribute. */
export interface Clause {
  attribute: string;
  op: Operator;
  values: string[];
}

/** Limits a targeting rule to a sticky percentage of the contexts it matches. */
export interface Rollout {
  /** Portion of matching contexts, in [0, 100], that receive the rule's value. */
  percentage: number;
  /** Attribute to bucket on; defaults to `key`. */
  bucketBy?: string;
  /** Bucketing seed; defaults to the flag key. */
  seed?: string;
}

/** Serves `value` to contexts that match every clause and fall within the rollout. */
export interface TargetingRule {
  id?: string;
  description?: string;
  clauses?: Clause[];
  value: unknown;
  rollout?: Rollout;
}

/** A flag: a typed default value plus ordered targeting rules. */
export interface Flag {
  type: FlagType;
  value: unknown;
  targeting?: TargetingRule[];
}

/** A namespace of flags, as served by `GET /v1/chambers/<path>`. */
export interface Chamber {
  rules: Record<string, Flag | null>;
}

/**
 * Who flags are evaluated for. `key` identifies the subject (a user id, for
 * example); it is the `key` attribute and the default rollout bucketing
 * attribute.
 */
export interface EvaluationContext {
  key?: string;
  attributes?: Record<string, string>;
}

/** The value a flag resolves to for a context, without its rules. */
export interface EvaluatedFlag {
  type: FlagType;
  value: unknown;
}

/** Flag results keyed by flag name. */
export type EvaluatedFlags = Record<string, EvaluatedFlag>;
