// Type imports only, so node:test can run this file with Node's type stripping.
import type { Me, PathRole, Principal, Role } from '../models/response';

// What the caller may do on each chamber, worked out from /v1/auth/me as the server works it out (RoleAt in
// auth/principal.go). It only decides which controls the UI offers: the server checks every request.

const rank: Record<Role, number> = { none: 0, viewer: 1, editor: 2, admin: 3 };

/** Whether role includes everything min allows. */
export const atLeast = (role: Role, min: Role) => rank[role] >= rank[min];

/**
 * Whether the chamber at path is prefix or beneath it: "/prod/" covers "/prod/" and "/prod/web/", but not
 * "/production/". Both are chamber paths with a trailing slash.
 */
export const covers = (prefix: string, path: string) => path.startsWith(prefix);

/**
 * The caller's role on the chamber at path (with a trailing slash). A signed-in user has the highest of their role on
 * every chamber and the roles of their paths that cover it. Without a signed-in user (the server has no sign-in),
 * everyone may do what the server reports.
 */
export const roleAt = (me: Me, path: string): Role => {
  if (!me.user) return me.isAdmin ? 'admin' : me.canEdit ? 'editor' : 'viewer';
  let role = me.user.role;
  for (const entry of me.user.paths ?? []) {
    if (rank[entry.role] > rank[role] && covers(entry.path, path)) role = entry.role;
  }
  return role;
};

/** Whether the caller may read the chamber at path, its history, and the chambers beneath it. */
export const canReadAt = (me: Me, path: string) => atLeast(roleAt(me, path), 'viewer');

/** Whether the caller may create, change, and delete the chamber at path, and the chambers beneath it. */
export const canEditAt = (me: Me, path: string) => atLeast(roleAt(me, path), 'editor');

/** The signed-in user's paths whose role includes min, sorted by path. */
export const pathsWith = (me: Me, min: Role): PathRole[] =>
  (me.user?.paths ?? []).filter((entry) => atLeast(entry.role, min));

/** The signed-in user's paths strictly beneath path whose role includes min. */
export const pathsBeneath = (me: Me, path: string, min: Role): PathRole[] =>
  pathsWith(me, min).filter((entry) => entry.path !== path && covers(path, entry.path));

/** A signed-in user's roles in words: "viewer of every chamber, editor of /prod/tenants/". */
export const describeRoles = (user: Principal): string =>
  [
    ...(user.role === 'none' ? [] : [`${user.role} of every chamber`]),
    ...(user.paths ?? []).map((entry) => `${entry.role} of ${entry.path}`),
  ].join(', ');
