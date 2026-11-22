// Type imports only, so node:test can run this file with Node's type stripping.
import type { Me, PathRole, Principal, Role } from '../models/response';

// What the caller may do on each chamber, worked out from /v1/auth/me as the server works it out (RoleAt in
// auth/principal.go). It only decides which controls the UI offers: the server checks every request.

const rank: Record<Role, number> = { none: 0, viewer: 1, editor: 2, admin: 3 };

const atLeast = (role: Role, min: Role) => rank[role] >= rank[min];

/** Whether the chamber at path is prefix or beneath it ("/prod/" covers "/prod/web/", not "/production/"). */
const covers = (prefix: string, path: string) => path.startsWith(prefix);

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

/** Whether the caller may create, change, and delete the chamber at path, and the chambers beneath it. */
export const canEditAt = (me: Me, path: string) => atLeast(roleAt(me, path), 'editor');

/** The signed-in user's paths strictly beneath path where they may edit, sorted by path. */
export const editorPathsBeneath = (me: Me, path: string): PathRole[] =>
  (me.user?.paths ?? []).filter((e) => atLeast(e.role, 'editor') && e.path !== path && covers(path, e.path));

/** A signed-in user's roles in words: "viewer of every chamber, editor of /prod/tenants/". */
export const describeRoles = (user: Principal): string =>
  [
    ...(user.role === 'none' ? [] : [`${user.role} of every chamber`]),
    ...(user.paths ?? []).map((entry) => `${entry.role} of ${entry.path}`),
  ].join(', ');
