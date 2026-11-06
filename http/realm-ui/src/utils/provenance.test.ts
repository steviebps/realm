/// <reference types="node" />
// The Node types are for node:test only; the app itself runs in the browser.
// Run with `npm test` (Node strips the types; Node 22.18 or later).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Lineage } from '../models/response.ts';
import { ancestorsOf, provenance } from './provenance.ts';

const bool = (value: boolean) => ({ type: 'boolean' as const, value });
const num = (value: number) => ({ type: 'number' as const, value });

/** /prod/us-east/canary/ in the sample tree, as the server reports it. */
const canary: Lineage = {
  inheritable: true,
  inheritedByChildren: true,
  layers: [
    { path: '/prod/us-east/canary/', status: 'ok', rules: { 'checkout.newFlow': bool(true) } },
    {
      path: '/prod/us-east/',
      status: 'ok',
      rules: { 'rate.limit': num(1000), 'region.failover': bool(true) },
    },
    {
      path: '/prod/',
      status: 'ok',
      rules: {
        'checkout.newFlow': { ...bool(false), targeting: [{ value: true, rollout: { percentage: 25 } }] },
        'prod.audit': bool(true),
        'rate.limit': num(500),
      },
    },
  ],
  sources: {
    'checkout.newFlow': '/prod/us-east/canary/',
    'prod.audit': '/prod/',
    'rate.limit': '/prod/us-east/',
    'region.failover': '/prod/us-east/',
  },
};

void test('ancestorsOf lists every ancestor, nearest first', () => {
  assert.deepEqual(ancestorsOf('/a/b/c/'), ['/a/b/', '/a/', '/']);
  assert.deepEqual(ancestorsOf('/a/'), ['/']);
  assert.deepEqual(ancestorsOf('/'), []);
});

void test('attributes each flag to the chamber that serves it', () => {
  const p = provenance(canary);
  assert.deepEqual(Object.keys(p.own), ['checkout.newFlow']);
  assert.deepEqual(
    p.own['checkout.newFlow'].overrides.map((d) => d.path),
    ['/prod/']
  );
  assert.deepEqual(Object.keys(p.inherited ?? {}).sort(), ['prod.audit', 'rate.limit', 'region.failover']);
  assert.equal(p.inherited?.['rate.limit'].source, '/prod/us-east/');
  assert.deepEqual(p.inherited?.['rate.limit'].overrides, [{ path: '/prod/', flag: num(500) }]);
  assert.deepEqual(p.inherited?.['region.failover'].overrides, []);
});

void test('stacks every ancestor and marks the ones the server does not consult', () => {
  const [usEast, prod, root] = provenance(canary).stack;
  assert.equal(usEast.kind, 'layer');
  if (usEast.kind !== 'layer' || prod.kind !== 'layer') return;
  assert.equal(usEast.distance, 1);
  assert.deepEqual(
    usEast.served.map((s) => s.name),
    ['rate.limit', 'region.failover']
  );
  assert.deepEqual(usEast.overridden, []);
  assert.equal(prod.distance, 2);
  assert.equal(prod.defined, 3);
  assert.deepEqual(
    prod.served.map((s) => s.name),
    ['prod.audit']
  );
  assert.deepEqual(prod.overridden, [
    { name: 'checkout.newFlow', by: '/prod/us-east/canary/' },
    { name: 'rate.limit', by: '/prod/us-east/' },
  ]);
  assert.deepEqual(root, { kind: 'notInherited', path: '/', distance: 3 });
});

void test('a missing parent is a layer with nothing in it', () => {
  const p = provenance({
    inheritable: true,
    inheritedByChildren: true,
    layers: [
      { path: '/staging/eu/', status: 'ok', rules: { 'ui.theme': { type: 'string', value: 'dark' } } },
      { path: '/staging/', status: 'missing' },
    ],
    sources: { 'ui.theme': '/staging/eu/' },
  });
  assert.deepEqual(p.inherited, {});
  assert.deepEqual(
    p.stack.map((e) => [e.kind, e.path]),
    [
      ['layer', '/staging/'],
      ['notInherited', '/'],
    ]
  );
  const [staging] = p.stack;
  assert.ok(staging.kind === 'layer' && staging.status === 'missing' && staging.defined === 0);
});

void test('a broken view attributes nothing', () => {
  const p = provenance({
    ...canary,
    layers: [canary.layers[0], canary.layers[1], { path: '/prod/', status: 'invalid', error: 'could not parse' }],
    sources: null,
    error: 'could not parse parent chamber /prod/',
  });
  assert.equal(p.inherited, null);
  assert.deepEqual(p.own, {});
  for (const entry of p.stack) {
    if (entry.kind === 'layer') assert.deepEqual([entry.served, entry.overridden], [[], []]);
  }
  const [usEast] = p.stack;
  assert.ok(usEast.kind === 'layer' && usEast.defined === 2);
});

void test('an empty chamber is not broken', () => {
  const p = provenance({
    inheritable: true,
    inheritedByChildren: true,
    layers: [
      { path: '/empty/child/', status: 'ok' },
      { path: '/empty/', status: 'missing' },
    ],
    sources: {},
  });
  assert.deepEqual(p.inherited, {});
  assert.deepEqual(p.own, {});
});

void test('without inheritance every ancestor is not inherited', () => {
  const p = provenance({
    inheritable: false,
    inheritedByChildren: false,
    layers: [canary.layers[0]],
    sources: { 'checkout.newFlow': '/prod/us-east/canary/' },
  });
  assert.deepEqual(p.own['checkout.newFlow'].overrides, []);
  assert.deepEqual(
    p.stack.map((e) => e.kind),
    ['notInherited', 'notInherited', 'notInherited']
  );
});

void test('the root has no ancestors', () => {
  const p = provenance({
    inheritable: true,
    inheritedByChildren: false,
    layers: [{ path: '/', status: 'ok', rules: { a: bool(true) } }],
    sources: { a: '/' },
  });
  assert.deepEqual(p.stack, []);
  assert.deepEqual(Object.keys(p.own), ['a']);
});

void test('flag keys never match Object.prototype members', () => {
  const p = provenance({
    inheritable: true,
    inheritedByChildren: true,
    layers: [
      { path: '/a/b/', status: 'ok', rules: { constructor: bool(true) } },
      { path: '/a/', status: 'ok', rules: { toString: bool(false) } },
    ],
    sources: { constructor: '/a/b/', toString: '/a/' },
  });
  // widened to string, as keys from the server are, so TypeScript doesn't see Object's members
  const [ctor, str]: string[] = ['constructor', 'toString'];
  assert.deepEqual(p.own[ctor].overrides, []);
  assert.deepEqual(p.inherited?.[str].overrides, []);
  const [a] = p.stack;
  assert.ok(a.kind === 'layer');
  assert.deepEqual(
    a.served.map((s) => s.name),
    ['toString']
  );
  assert.deepEqual(a.overridden, []);
});
