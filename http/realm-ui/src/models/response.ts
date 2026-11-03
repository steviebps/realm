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
