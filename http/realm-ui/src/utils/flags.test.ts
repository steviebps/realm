/// <reference types="node" />
// Run with `npm test` (Node strips the types; Node 22.18 or later).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Flag } from '../models/response.ts';
import { rulesOf, targetingPaused, validateFlag, withRules, withSealed, withTargetingPaused } from './flags.ts';

void test('withSealed writes the seal where the server does, and removes it entirely', () => {
  const flag: Flag = { type: 'boolean', value: false, targeting: [{ value: true }] };
  const sealed = withSealed(flag, true);
  assert.equal(JSON.stringify(sealed), '{"type":"boolean","value":false,"sealed":true,"targeting":[{"value":true}]}');
  assert.equal(JSON.stringify(withSealed(sealed, false)), JSON.stringify(flag));
  assert.equal(JSON.stringify(withSealed(sealed, true)), JSON.stringify(sealed));
  assert.equal('sealed' in withSealed({ type: 'number', value: 1 }, false), false);
  // the input is not changed
  assert.equal(flag.sealed, undefined);
});

void test('withTargetingPaused moves the rules between targeting and pausedTargeting, and back', () => {
  const rule = {
    id: 'pilots',
    clauses: [{ attribute: 'locationId', op: 'in' as const, values: ['loc-a'] }],
    value: true,
  };
  const flag: Flag = { type: 'boolean', value: false, sealed: true, targeting: [rule] };
  const off = withTargetingPaused(flag, true);
  assert.equal(
    JSON.stringify(off),
    '{"type":"boolean","value":false,"sealed":true,"pausedTargeting":[{"id":"pilots","clauses":[{"attribute":"locationId","op":"in","values":["loc-a"]}],"value":true}]}'
  );
  assert.equal(targetingPaused(off), true);
  assert.deepEqual(rulesOf(off), [rule]);
  assert.equal(JSON.stringify(withTargetingPaused(off, false)), JSON.stringify(flag));
  assert.equal(JSON.stringify(withTargetingPaused(off, true)), JSON.stringify(off));
  // the input is not changed
  assert.deepEqual(flag.targeting, [rule]);
  assert.equal('pausedTargeting' in flag, false);
});

void test('a flag without rules is never paused, and has neither list', () => {
  const flag: Flag = { type: 'number', value: 1 };
  assert.equal(targetingPaused(flag), false);
  assert.equal(JSON.stringify(withTargetingPaused(flag, true)), '{"type":"number","value":1}');
  assert.equal(JSON.stringify(withRules({ ...flag, pausedTargeting: [] }, [], true)), '{"type":"number","value":1}');
});

void test('rules in targeting win over paused ones, as SDKs serve them', () => {
  const flag: Flag = { type: 'string', value: 'a', targeting: [{ value: 'on' }], pausedTargeting: [{ value: 'off' }] };
  assert.equal(targetingPaused(flag), false);
  assert.deepEqual(rulesOf(flag), [{ value: 'on' }]);
  // editing keeps one list, so the server accepts the flag
  assert.equal(
    JSON.stringify(withRules(flag, rulesOf(flag), false)),
    '{"type":"string","value":"a","targeting":[{"value":"on"}]}'
  );
});

void test('validateFlag checks paused rules', () => {
  const flag: Flag = { type: 'boolean', value: false, pausedTargeting: [{ value: true, schedule: {} }] };
  assert.deepEqual(validateFlag(flag), ['Rule 1: set a start or an end for the schedule.']);
});
