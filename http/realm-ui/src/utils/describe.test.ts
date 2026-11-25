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
