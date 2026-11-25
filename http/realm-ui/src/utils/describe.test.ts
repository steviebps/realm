/// <reference types="node" />
// Run with `npm test` (Node strips the types; Node 22.18 or later).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Chamber } from '../models/response.ts';
import { diffChambers } from './describe.ts';

const chamber = (rules: Chamber['rules']): Chamber => ({ rules });

void test('diffChambers shows a change of type', () => {
  const [change] = diffChambers(
    chamber({ limit: { type: 'number', value: 1 } }),
    chamber({ limit: { type: 'string', value: 1 } as never }),
    {}
  );
  assert.equal(change?.kind, 'changed');
  assert.deepEqual(change?.lines, [
    { kind: 'remove', text: 'Type: number' },
    { kind: 'add', text: 'Type: string' },
  ]);
});

void test('diffChambers shows a change of a rule id or description', () => {
  const before = chamber({ on: { type: 'boolean', value: false, targeting: [{ id: 'a', value: true }] } });
  const renamed = chamber({ on: { type: 'boolean', value: false, targeting: [{ id: 'b', value: true }] } });
  const described = chamber({
    on: { type: 'boolean', value: false, targeting: [{ id: 'a', description: 'beta users', value: true }] },
  });
  const [byId] = diffChambers(before, renamed, {});
  assert.deepEqual(
    byId?.lines.map((l) => l.text),
    ['Rule 1 (a): If everyone → serve true', 'Rule 1 (b): If everyone → serve true']
  );
  const [byDescription] = diffChambers(before, described, {});
  assert.equal(byDescription?.lines[1]?.text, 'Rule 1 (a): If everyone → serve true — beta users');
});

void test('diffChambers lists added, removed, and unchanged flags', () => {
  const changes = diffChambers(
    chamber({ gone: { type: 'boolean', value: true }, kept: { type: 'boolean', value: true } }),
    chamber({ kept: { type: 'boolean', value: true }, fresh: { type: 'number', value: 2 } }),
    {}
  );
  assert.deepEqual(
    changes.map((c) => [c.flag, c.kind]),
    [
      ['fresh', 'added'],
      ['gone', 'removed'],
    ].sort()
  );
});

void test('diffChambers lists the flags written as null that saving removes, and what is served instead', () => {
  const changes = diffChambers(
    chamber({ kept: { type: 'boolean', value: true } }),
    chamber({ kept: { type: 'boolean', value: false }, again: { type: 'number', value: 1 } }),
    {},
    [
      { name: 'beta', inherits: { path: '/', flag: { type: 'boolean', value: true } } },
      { name: 'gamma' },
      { name: 'delta', unknown: true },
      // defined again by the change: shown as added
      { name: 'again' },
    ]
  );
  assert.deepEqual(
    changes.map((c) => [c.flag, c.kind]),
    [
      ['again', 'added'],
      ['beta', 'removed'],
      ['delta', 'removed'],
      ['gamma', 'removed'],
      ['kept', 'changed'],
    ]
  );
  const lines = Object.fromEntries(changes.map((c) => [c.flag, c.lines]));
  assert.deepEqual(lines.beta, [
    { kind: 'remove', text: "Written as null: serves nothing here, hiding /'s definition" },
    { kind: 'info', text: "Saving removes it, so this chamber serves /'s definition:" },
    { kind: 'add', text: 'No targeting: every context gets true.' },
  ]);
  assert.deepEqual(lines.gamma, [
    { kind: 'remove', text: 'Written as null' },
    { kind: 'info', text: 'Saving removes it; what this chamber serves for it does not change' },
  ]);
  assert.deepEqual(lines.delta, [
    { kind: 'remove', text: 'Written as null' },
    { kind: 'info', text: "Saving removes it, so this chamber serves a parent chamber's definition, if one has it" },
  ]);
});
