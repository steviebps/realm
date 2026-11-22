/// <reference types="node" />
// The Node types are for node:test only; the app itself runs in the browser.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Me, PathRole, Role } from '../models/response.ts';
import { canEditAt, describeRoles, editorPathsBeneath, roleAt } from './access.ts';

const signedIn = (role: Role, paths?: PathRole[]): Me => ({
  authEnabled: true,
  user: { kind: 'user', id: 'someone@example.com', role, paths },
  canEdit: role === 'editor' || role === 'admin',
  isAdmin: role === 'admin',
  providers: [],
});

void test("a user's role on a chamber is the highest among the paths that cover it", () => {
  // a viewer of every chamber who edits the tenants' chambers
  const support = signedIn('viewer', [{ path: '/prod/tenants/', role: 'editor' }]);
  // a user who may use only the regions' chambers
  const compliance = signedIn('none', [{ path: '/prod/regions/', role: 'editor' }]);
  const cases: [Me, string, Role][] = [
    [support, '/', 'viewer'],
    [support, '/prod/', 'viewer'],
    [support, '/prod/tenants/', 'editor'],
    [support, '/prod/tenants/acme/eu/', 'editor'],
    [support, '/prod/tenants-old/', 'viewer'],
    [compliance, '/', 'none'],
    [compliance, '/prod/', 'none'],
    [compliance, '/prod/regions/eu/', 'editor'],
    [signedIn('admin'), '/prod/', 'admin'],
  ];
  for (const [me, path, want] of cases) {
    assert.equal(roleAt(me, path), want, `${me.user?.role} ${JSON.stringify(me.user?.paths)} at ${path}`);
  }
  assert.ok(!canEditAt(support, '/prod/'));
  assert.ok(canEditAt(support, '/prod/tenants/acme/'));
});

void test('without sign-in, everyone may do what the server reports', () => {
  const open: Me = { authEnabled: false, user: null, canEdit: true, isAdmin: true, providers: [] };
  assert.equal(roleAt(open, '/prod/'), 'admin');
  assert.ok(canEditAt(open, '/'));
});

void test('the paths beneath a chamber where a user may edit', () => {
  const me = signedIn('none', [
    { path: '/prod/regions/', role: 'editor' },
    { path: '/prod/tenants/', role: 'viewer' },
    { path: '/staging/', role: 'editor' },
  ]);
  assert.deepEqual(
    editorPathsBeneath(me, '/prod/').map((e) => e.path),
    ['/prod/regions/']
  );
  assert.deepEqual(
    editorPathsBeneath(me, '/').map((e) => e.path),
    ['/prod/regions/', '/staging/']
  );
  // not the chamber itself
  assert.deepEqual(editorPathsBeneath(me, '/staging/'), []);
});

void test("a user's roles in words", () => {
  const support = signedIn('viewer', [{ path: '/prod/tenants/', role: 'editor' }]).user!;
  assert.equal(describeRoles(support), 'viewer of every chamber, editor of /prod/tenants/');
  const compliance = signedIn('none', [{ path: '/prod/regions/', role: 'editor' }]).user!;
  assert.equal(describeRoles(compliance), 'editor of /prod/regions/');
});
