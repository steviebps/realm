/// <reference types="node" />
// Run with `npm test` (Node strips the types; Node 22.18 or later).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { LoadedChamber } from '../api.ts';
import type { Chamber, Flag } from '../models/response.ts';
import {
  changedFlags,
  changes,
  checkAdd,
  confirm,
  overrideSeed,
  propose,
  rowActions,
  type SessionIO,
} from './chamberSession.ts';
import type { Provenance } from './provenance.ts';

const loaded = (rules: Chamber['rules'], etag: string): LoadedChamber => ({
  own: { rules },
  etag,
  lineage: null,
  lineageError: null,
  nullFlags: [],
});

class Conflict extends Error {}

// io saves when the ETag matches current's, and otherwise refuses with a conflict; load returns current.
const io = (current: { value: LoadedChamber }, saved: Chamber[] = []): SessionIO => ({
  save: (chamber, etag) => {
    if (etag !== current.value.etag) return Promise.reject(new Conflict());
    saved.push(chamber);
    return Promise.resolve();
  },
  load: () => Promise.resolve(current.value),
  isConflict: (err) => err instanceof Conflict,
  messageOf: (err) => (err instanceof Error ? err.message : String(err)),
});

const on = (value: boolean): Flag => ({ type: 'boolean', value });

void test('confirm saves against the ETag loaded', async () => {
  const current = { value: loaded({ a: on(false) }, '"1"') };
  const saved: Chamber[] = [];
  const p = propose(current.value, 'Update a', changes.put('a', on(true)));
  assert.deepEqual(await confirm(p, io(current, saved)), { saved: true });
  assert.deepEqual(saved, [{ rules: { a: on(true) } }]);
});

void test('a conflict re-applies the change to the latest chamber and names flags it overwrites', async () => {
  const base = loaded({ a: on(false), b: on(false) }, '"1"');
  const current = { value: loaded({ a: on(false), b: on(true), c: on(true) }, '"2"') };
  // the change rewrites b, which someone else changed too, and adds d
  const mine: Flag = { type: 'boolean', value: false, description: 'mine' };
  const p = propose(base, 'Update', (c) => changes.put('d', on(true))(changes.put('b', mine)(c)));
  const result = await confirm(p, io(current));
  assert.equal(result.saved, false);
  if (result.saved) return;
  assert.equal(result.proposal.rebased, true);
  assert.equal(result.proposal.base.etag, '"2"');
  assert.deepEqual(Object.keys(result.proposal.after.rules).sort(), ['a', 'b', 'c', 'd']);
  assert.deepEqual(result.proposal.overwrites, ['b']);
  assert.equal(result.reloaded, current.value);

  // confirming the rebased proposal saves it
  assert.deepEqual(await confirm(result.proposal, io(current)), { saved: true });
});

void test('restoring a revision names the flags someone else added or deleted since', async () => {
  const base = loaded({ a: on(false), x: on(true) }, '"1"');
  // they added c and deleted x; restoring discards both
  const current = { value: loaded({ a: on(false), c: on(true) }, '"2"') };
  const p = propose(base, 'Restore', changes.replace({ rules: { a: on(true), x: on(true) } }));
  const result = await confirm(p, io(current));
  assert.equal(result.saved, false);
  if (result.saved) return;
  assert.deepEqual(result.proposal.overwrites, ['c', 'x']);
});

void test('a rebased change that builds on their change to the same flag overwrites nothing', async () => {
  const ruled = (value: boolean): Flag => ({ type: 'boolean', value, targeting: [{ value: true }] });
  const base = loaded({ a: ruled(false) }, '"1"');
  // they changed a's default; pausing a's targeting keeps it
  const current = { value: loaded({ a: ruled(true) }, '"2"') };
  const result = await confirm(propose(base, 'Pause a', changes.pause('a', true)), io(current));
  assert.equal(result.saved, false);
  if (result.saved) return;
  assert.equal(result.proposal.after.rules.a?.value, true);
  assert.equal(result.proposal.overwrites, undefined);
});

void test('deleting a flag someone else changed overwrites their change', async () => {
  const base = loaded({ a: on(false) }, '"1"');
  const current = { value: loaded({ a: on(true) }, '"2"') };
  const result = await confirm(propose(base, 'Delete a', changes.remove('a')), io(current));
  assert.deepEqual(!result.saved && result.proposal.overwrites, ['a']);
});

void test('a rebased change that touches only flags nobody else changed overwrites nothing', async () => {
  const base = loaded({ a: on(false) }, '"1"');
  const current = { value: loaded({ a: on(false), z: on(true) }, '"2"') };
  const result = await confirm(propose(base, 'Update a', changes.put('a', on(true))), io(current));
  assert.equal(!result.saved && result.proposal.overwrites, undefined);
});

void test('a failed reload after a conflict keeps the old base', async () => {
  const base = loaded({ a: on(false) }, '"1"');
  const current = { value: loaded({ a: on(true) }, '"2"') };
  const failing: SessionIO = { ...io(current), load: () => Promise.reject(new Error('offline')) };
  const p = propose(base, 'Update a', changes.put('a', on(true)));
  const result = await confirm(p, failing);
  assert.equal(result.saved, false);
  if (result.saved) return;
  assert.equal(result.proposal.base, base);
  assert.match(result.proposal.error ?? '', /loading their change failed: offline/);
  assert.equal(result.proposal.rebased, undefined);
});

void test('another failure is reported without reloading', async () => {
  const base = loaded({}, '"1"');
  const broken: SessionIO = { ...io({ value: base }), save: () => Promise.reject(new Error('refused')) };
  const result = await confirm(propose(base, 'Add a', changes.put('a', on(true))), broken);
  assert.deepEqual(!result.saved && [result.proposal.error, result.reloaded], ['refused', undefined]);
});

void test('changedFlags names flags added, removed, or changed', () => {
  assert.deepEqual(
    changedFlags(
      { rules: { a: on(true), b: on(true), c: on(true) } },
      { rules: { a: on(true), b: on(false), d: on(true) } }
    ),
    ['b', 'c', 'd']
  );
  assert.deepEqual(changedFlags(null, { rules: { a: on(true) } }), ['a']);
});

const prov = (p: Partial<Provenance>): Provenance => ({ own: {}, inherited: {}, sealedBy: {}, ...p }) as Provenance;

void test('checkAdd refuses empty, existing, and sealed names', () => {
  const own: Chamber = { rules: { here: on(true) } };
  const inherited = { name: 'up', flag: on(false), source: '/', overrides: [], hidden: [] };
  const p = prov({ sealedBy: { locked: '/' }, inherited: { up: inherited } });
  assert.equal(checkAdd(own, p, '  ').allowed, false);
  assert.deepEqual(checkAdd(own, p, 'here'), {
    name: 'here',
    inherited: undefined,
    sealedBy: undefined,
    exists: true,
    allowed: false,
  });
  assert.equal(checkAdd(own, p, 'locked').sealedBy, '/');
  assert.equal(checkAdd(own, p, 'locked').allowed, false);
  assert.equal(checkAdd(own, p, ' up ').inherited, inherited);
  assert.equal(checkAdd(own, p, 'up').allowed, true);
});

void test('overrideSeed keeps type, value, and targeting, and leaves the rest with the parent', () => {
  const parent: Flag = {
    type: 'number',
    value: 24,
    targeting: [{ value: 48 }],
    constraints: { maximum: 72 },
    description: 'hold hours',
    owner: 'ops',
    lifecycle: 'permanent',
    sealed: true,
  };
  assert.deepEqual(overrideSeed(parent), { type: 'number', value: 24, targeting: [{ value: 48 }] });
});

void test('rowActions hides edits for a sealed definition and offers pause only with rules', () => {
  const own: Chamber = {
    rules: { plain: on(true), ruled: { type: 'boolean', value: false, targeting: [{ value: true }] } },
  };
  assert.deepEqual(rowActions(own, prov({}), 'plain'), {
    hiddenBy: undefined,
    editable: true,
    pausable: false,
    paused: false,
  });
  assert.equal(rowActions(own, prov({}), 'ruled').pausable, true);
  assert.deepEqual(rowActions(own, prov({ sealedBy: { ruled: '/' } }), 'ruled'), {
    hiddenBy: '/',
    editable: false,
    pausable: false,
    paused: false,
  });
});

void test('pausing a flag removed meanwhile changes nothing', () => {
  assert.deepEqual(changes.pause('gone', true)({ rules: { a: on(true) } }), { rules: { a: on(true) } });
});
