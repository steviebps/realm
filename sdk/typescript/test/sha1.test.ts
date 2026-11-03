import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { sha1 } from '../src/sha1.js';

const hex = (b: Uint8Array) => Buffer.from(b).toString('hex');

describe('sha1', () => {
  it.each([
    ['', 'da39a3ee5e6b4b0d3255bfef95601890afd80709'],
    ['abc', 'a9993e364706816aba3e25717850c26c9cd0d89d'],
    ['The quick brown fox jumps over the lazy dog', '2fd4e1c67a2d28fced849ee1bb76e7391b93eb12'],
  ])('hashes %j', (input, want) => {
    expect(hex(sha1(new TextEncoder().encode(input)))).toBe(want);
  });

  it('matches node:crypto across block boundaries', () => {
    for (let n = 0; n < 300; n++) {
      const data = new Uint8Array(n).map((_, i) => (i * 31 + n) & 0xff);
      expect(hex(sha1(data)), `length ${n}`).toBe(createHash('sha1').update(data).digest('hex'));
    }
  });
});
