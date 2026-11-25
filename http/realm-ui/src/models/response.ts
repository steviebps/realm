export type ListResponse = {
  data?: Array<string>;
  errors?: Array<string>;
};

// The wire types realm's API shares with the TypeScript SDK are the SDK's
// (sdk/typescript/src/types.ts), so the two cannot drift; only the shapes the
// UI alone reads are declared here. Type-only, so nothing of the SDK reaches the
// bundle, and node:test can still strip these imports.
export type {
  Clause,
  Flag,
  FlagType,
  Lifecycle,
  Operator,
  Rollout,
  Schedule,
  Schema,
  TargetingRule,
} from '../../../../sdk/typescript/src/types.ts';
import type { Chamber as WireChamber, Flag } from '../../../../sdk/typescript/src/types.ts';

/** A chamber as the server sends it, whose flags may be null (see withoutNullFlags in utils/flags). */
export type { WireChamber };

/** A flag's value: any JSON, as a custom flag may hold arrays and null. */
export type FlagValue = Flag['value'];

/** Every flag a chamber defines, by key. */
export type Rules = Record<string, Flag>;

/**
 * A chamber as the UI holds it: the server's, without flags written as null, which writes now refuse (api.ts drops
 * them as it loads a chamber, and saving lists each one it removes: see NullFlag in utils/provenance).
 */
export type Chamber = { rules: Rules };

export type ChamberResponse = {
  data?: WireChamber;
  errors?: Array<string>;
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
  /**
   * Flag key → path of the farthest parent that seals it: this chamber cannot define those flags. Omitted when
   * nothing is sealed (and by servers that predate seals).
   */
  sealedBy?: Record<string, string>;
  /**
   * Sealed flag key → paths of the chambers (this one included) whose definitions the seal hides, nearest first.
   * Omitted when there are none.
   */
  hidden?: Record<string, string[]>;
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

/** A role a signed-in user holds on the chambers at and beneath a path. */
export type PathRole = {
  /** A chamber path, with a trailing slash. */
  path: string;
  role: Role;
};

export type Principal = {
  kind: 'user' | 'key' | 'anonymous';
  id: string;
  name?: string;
  provider?: string;
  /** A signed-in user's role on every chamber. */
  role: Role;
  /**
   * The higher roles a signed-in user holds on the chambers at and beneath some paths, sorted by path. Each is higher
   * than `role` and than the roles of the paths above its own. Omitted when there are none.
   */
  paths?: PathRole[];
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
  /**
   * Whether the caller may change every chamber. A signed-in user may also change the chambers beneath their paths
   * with an editor role: use canEditAt (utils/access) for a chamber.
   */
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
