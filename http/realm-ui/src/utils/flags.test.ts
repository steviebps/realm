/// <reference types="node" />
// Run with `npm test` (Node strips the types; Node 22.18 or later).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Flag } from '../models/response.ts';
import { withSealed } from './flags.ts';

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
