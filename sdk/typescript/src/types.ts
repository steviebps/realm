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

/**
 * Limits a targeting rule to a window of time: from `start` (inclusive) until
 * `end` (exclusive), as RFC 3339 timestamps. An omitted bound leaves that side
 * of the window open.
 */
export interface Schedule {
  start?: string;
  end?: string;
}

/**
 * Serves `value` to contexts that match every clause and fall within the
 * rollout, while the schedule (if any) is in effect.
 */
export interface TargetingRule {
  id?: string;
  description?: string;
  clauses?: Clause[];
  value: unknown;
  rollout?: Rollout;
  schedule?: Schedule;
}

/**
 * Whether a flag is meant to be removed once it has served its purpose
 * (`temporary`: a release toggle or an experiment) or to stay (`permanent`: a
 * setting or a kill switch). A newer server may send others.
 */
export type Lifecycle = 'temporary' | 'permanent';

/**
 * The subset of JSON Schema a flag's values are checked against when it is
 * written. The server refuses any other keyword, so none is ignored. Keywords
 * about one JSON type (`minimum`, `maxLength`, `properties`, …) say nothing
 * about values of another. A newer server may send keywords this version does
 * not list.
 */
export interface Schema {
  type?: 'object' | 'array' | 'string' | 'number' | 'integer' | 'boolean' | 'null';
  /** The only values allowed, compared as JSON. */
  enum?: unknown[];
  /** Inclusive bounds on numbers. */
  minimum?: number;
  maximum?: number;
  /** Numbers must be multiples of it (1 allows only whole numbers). */
  multipleOf?: number;
  /** Inclusive bounds on the length of strings, in Unicode code points. */
  minLength?: number;
  maxLength?: number;
  /** The schemas of an object's members, by name. */
  properties?: Record<string, Schema>;
  /** The members every object must have. */
  required?: string[];
  /** When false, objects may have no members but those `properties` names. */
  additionalProperties?: boolean;
  /** The schema of every element of an array. */
  items?: Schema;
}

/** A flag: a typed default value plus ordered targeting rules. */
export interface Flag {
  type: FlagType;
  value: unknown;
  /** What the flag is for. */
  description?: string;
  /** Who is responsible for the flag: a team or a person. */
  owner?: string;
  /** Whether the flag is meant to be removed once it has served its purpose. */
  lifecycle?: Lifecycle;
  /**
   * The values the flag may take: the server refuses a write whose default or
   * targeting rule values (paused or not) fall outside them, or outside the
   * constraints of the flag in any chamber above. Reads never check them.
   */
  constraints?: Schema;
  /**
   * Whether chambers below the one that defines the flag are kept from
   * overriding it. The server applies seals, so a fetched chamber already
   * serves the sealed definition.
   */
  sealed?: boolean;
  targeting?: TargetingRule[];
  /**
   * The flag's targeting rules while its targeting is turned off. They are
   * kept for when it is turned back on, but never evaluated: every context
   * gets the default `value`.
   */
  pausedTargeting?: TargetingRule[];
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

/**
 * Flags evaluated on the server for one context, for a page rendered there:
 * pass it to the BrowserClient that renders the page on the server and to
 * the one that hydrates it in the browser, so both serve the same values
 * from the first render. ServerClient.initialState makes one. It is plain
 * JSON; serializeInitialState writes it safely into an HTML page.
 */
export interface InitialState {
  /** Who the flags were evaluated for, as the server receives contexts. */
  context: EvaluationContext;
  /** Every flag's value for context, in the shape allFlags returns. */
  flags: EvaluatedFlags;
  /** The server's storage revision of the chamber evaluated, if it reported one. */
  revision?: number;
}
