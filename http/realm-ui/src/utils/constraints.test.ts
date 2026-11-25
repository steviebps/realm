/// <reference types="node" />
// Run with `npm test` (Node strips the types; Node 22.18 or later).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import type { Flag, Lineage, FlagType, Schema } from '../models/response.ts';
import {
  allowedValues,
  constraintProblems,
  describeConstraints,
  numberRange,
  parentBounds,
  formatValueError,
  parseConstraints,
  validate,
} from './constraints.ts';

// The cases the server runs too (pkg/schema_test.go), with the server's words.
const conformance = JSON.parse(
  readFileSync(new URL('../../../../sdk/conformance/constraints.json', import.meta.url), 'utf8')
) as {
  schemas: { name: string; flagType: FlagType; schema: unknown; error?: string }[];
  values: { name: string; schema: Schema; value: unknown; error?: string }[];
};

for (const c of conformance.schemas) {
  void test(`conformance: schema: ${c.name}`, () => {
    assert.equal(parseConstraints(c.schema, c.flagType).error, c.error);
  });
}

for (const c of conformance.values) {
  void test(`conformance: value: ${c.name}`, () => {
    const parsed = parseConstraints(c.schema, 'custom');
    assert.equal(parsed.error, undefined);
    const err = validate(parsed.schema, c.value);
    assert.equal(err && formatValueError(err), c.error);
  });
}

const lineage = (rules: Record<string, Record<string, Flag>>): Lineage => ({
  inheritable: true,
  layers: Object.entries(rules).map(([path, r]) => ({ path, status: 'ok', rules: r })),
  sources: {},
});

void test('parentBounds lists the parents that constrain a flag, farthest first', () => {
  const lin = lineage({
    '/t/a/': { hold: { type: 'number', value: 1, constraints: { maximum: 2 } } },
    '/t/': { hold: { type: 'number', value: 1, constraints: { maximum: 48 } } },
    '/': {
      hold: { type: 'number', value: 1, constraints: { minimum: 1, maximum: 72 } },
      other: { type: 'number', value: 1 },
    },
  });
  assert.deepEqual(parentBounds(lin, 'hold', 'number'), [
    { path: '/', schema: { minimum: 1, maximum: 72 } },
    { path: '/t/', schema: { maximum: 48 } },
  ]);
  assert.deepEqual(parentBounds(lin, 'other', 'number'), []);
  // another type is the server's to refuse
  assert.deepEqual(parentBounds(lin, 'hold', 'string'), []);
  assert.deepEqual(parentBounds(null, 'hold', 'number'), []);
});

void test('constraintProblems names each value a bound refuses, and the parent that sets it', () => {
  const flag: Flag = { type: 'number', value: 60, targeting: [{ value: 24 }, { value: 96 }] };
  assert.deepEqual(constraintProblems(flag, [{ path: '/', schema: { maximum: 72 } }, { schema: { maximum: 48 } }]), [
    'Default: 60 is greater than the maximum, 48.',
    'Rule 2: 96 is greater than the maximum, 72 (constraints of /).',
  ]);
  // paused rules are checked too
  assert.deepEqual(
    constraintProblems({ type: 'number', value: 1, pausedTargeting: [{ value: 0 }] }, [{ schema: { minimum: 1 } }]),
    ['Rule 1: 0 is less than the minimum, 1.']
  );
});

void test('inputs are bounded by every schema at once', () => {
  assert.deepEqual(
    numberRange([
      { minimum: 1, maximum: 72 },
      { maximum: 48, multipleOf: 1 },
    ]),
    { min: 1, max: 48, step: 1 }
  );
  assert.deepEqual(numberRange([{ type: 'integer' }]), { min: undefined, max: undefined, step: 1 });
  assert.deepEqual(numberRange([]), { min: undefined, max: undefined, step: undefined });
  assert.deepEqual(allowedValues([{ enum: ['a', 'b', 'c'] }, { enum: ['c', 'a'] }]), ['a', 'c']);
  assert.deepEqual(allowedValues([{ enum: ['short', 'much too long'] }, { maxLength: 5 }]), ['short']);
  assert.equal(allowedValues([{ maxLength: 5 }]), undefined);
});

void test('describeConstraints summarizes a schema in plain language', () => {
  assert.equal(
    describeConstraints({ minimum: 1, maximum: 72, multipleOf: 1 }, 'number'),
    'from 1 to 72, whole numbers'
  );
  assert.equal(describeConstraints({ maximum: 72, multipleOf: 0.5 }, 'number'), 'at most 72, multiples of 0.5');
  assert.equal(describeConstraints({ enum: ['standard', 'express'] }, 'string'), 'one of "standard", "express"');
  assert.equal(describeConstraints({ maxLength: 140 }, 'string'), 'up to 140 characters');
  assert.equal(describeConstraints({ minLength: 1, maxLength: 1 }, 'string'), '1 to 1 character');
  assert.equal(describeConstraints({}, 'number'), 'any value');
  assert.equal(describeConstraints({ type: 'object' }, 'custom'), 'matches {"type":"object"}');
});
