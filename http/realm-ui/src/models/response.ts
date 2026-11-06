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

/** A window of time, as RFC 3339 times: from start (inclusive) until end (exclusive). */
export type Schedule = {
  start?: string;
  end?: string;
};

export type TargetingRule = {
  id?: string;
  description?: string;
  clauses?: Clause[];
  value: FlagValue;
  rollout?: Rollout;
  schedule?: Schedule;
};

export type Flag = {
  type: RuleType;
  value: FlagValue;
  targeting?: TargetingRule[];
};

/**
 * How a chamber in a lineage was read: `ok` (its rules hold every flag it
 * defines), `missing` (no chamber at the path; it contributes nothing), or
 * `invalid` (it cannot be parsed; `error` says why).
 */
export type LayerStatus = 'ok' | 'missing' | 'invalid';

/** One chamber that a path's inherited view is built from. */
export type Layer = {
  /** Canonical path, with a trailing slash. */
  path: string;
  status: LayerStatus;
  /** Every flag the chamber defines (`ok` only; omitted when it has none). */
  rules?: Rules;
  /** Why the chamber cannot be parsed (`invalid` only). */
  error?: string;
};

/** The chamber and each parent chamber its inherited view consults, as the server reports them. */
export type Lineage = {
  /** Whether the server applies inheritance at all. */
  inheritable: boolean;
  /** The chamber itself, then each consulted parent, nearest first (missing ones included). */
  layers: Layer[];
  /** Flag key → path of the chamber whose definition is served here; null exactly when `error` is set. */
  sources: Record<string, string> | null;
  /** Why the inherited view cannot be built, as clients loading this chamber see it. */
  error?: string;
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

export type Role = 'none' | 'viewer' | 'editor' | 'admin';

export type Principal = {
  kind: 'user' | 'key' | 'anonymous';
  id: string;
  name?: string;
  provider?: string;
  role: Role;
};

export type Provider = {
  name: string;
  displayName: string;
  type: 'oidc' | 'github';
};

/** Who is signed in, and how to sign in when nobody is. */
export type Me = {
  authEnabled: boolean;
  user: Principal | null;
  /** Whether the caller may change chambers. */
  canEdit: boolean;
  /** Whether the caller may manage API keys and the catalog. */
  isAdmin: boolean;
  providers: Provider[];
};

export type KeyAccess = 'evaluate' | 'read' | 'write';

export type ApiKey = {
  id: string;
  name: string;
  access: KeyAccess;
  path: string;
  createdAt: string;
  createdBy: string;
};

/** A newly created key; the token is shown only once. */
export type CreatedKey = {
  key: ApiKey;
  token: string;
};
