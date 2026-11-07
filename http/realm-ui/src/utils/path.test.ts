/// <reference types="node" />
// The Node types are for node:test only; the app itself runs in the browser.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decodePath, encodePath } from './path.ts';

void test('chamber paths survive the URL', () => {
  for (const path of ['/', '/prod/web/', '/prod/https:evil.example/', '/a?b/#c/', '/a new one/', '/100%/', '/café/']) {
    const encoded = encodePath(path);
    assert.ok(!/[?#: ]/.test(encoded), `${encoded} keeps characters that change the URL's meaning`);
    assert.equal(decodePath(encoded), path);
  }
});

void test('a malformed escape is left as it is', () => {
  assert.equal(decodePath('/a%zz/b%20c/'), '/a%zz/b c/');
});
