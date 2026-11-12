import { afterEach, describe, expect, it, vi } from 'vitest';
import { Backoff, jitter, parseRevision, pollingInterval, RealmError, Transport } from '../src/transport.js';
import { fakeRealm } from './fake-realm.js';

let cleanup: (() => unknown)[] = [];
afterEach(async () => {
  for (const fn of cleanup.reverse()) await fn();
  cleanup = [];
});

// random makes Math.random return each of values in turn, then the last.
function random(...values: number[]) {
  const spy = vi.spyOn(Math, 'random');
  for (const v of values) spy.mockReturnValueOnce(v);
  spy.mockReturnValue(values.at(-1)!);
  cleanup.push(() => spy.mockRestore());
}

const seconds = (ms: number) => ms / 1000;

describe('Backoff', () => {
  it('doubles from a second up to 30 seconds, each delay a random value in [d/2, d)', () => {
    const steps = [1, 2, 4, 8, 16, 30, 30, 30];
    random(0);
    const shortest = new Backoff();
    expect(steps.map(() => seconds(shortest.next(new Error('down'))))).toEqual(steps.map((d) => d / 2));
    random(0.999);
    const longest = new Backoff();
    for (const d of steps) {
      const delay = seconds(longest.next(new Error('down')));
      expect(delay).toBeGreaterThan(d * 0.99);
      expect(delay).toBeLessThan(d);
    }
  });

  it('backs off up to five minutes after a refusal or a missing chamber', () => {
    random(0);
    for (const status of [401, 403, 404]) {
      const backoff = new Backoff();
      const refused = new RealmError('refused', status);
      const delays = Array.from({ length: 12 }, () => seconds(backoff.next(refused)));
      expect(delays).toEqual([0.5, 1, 2, 4, 8, 16, 32, 64, 128, 150, 150, 150]);
      // other failures still wait at most 30 seconds
      expect(seconds(backoff.next(new RealmError('unavailable', 503)))).toBe(15);
      // and a success starts over
      backoff.reset();
      expect(seconds(backoff.next(refused))).toBe(0.5);
    }
  });

  it('waits at least as long as Retry-After asks, and at most cap otherwise', () => {
    random(0);
    expect(new Backoff().next(new RealmError('busy', 503, 5_000))).toBe(5_000);
    expect(new Backoff().next(new RealmError('busy', 503, 5_000), 100)).toBe(5_000);
    expect(new Backoff().next(new Error('down'), 100)).toBe(50);
  });
});

describe('jitter', () => {
  it('spreads an interval by up to a tenth either way', () => {
    random(0, 0.5, 0.999);
    expect(jitter(1_000)).toBe(900);
    expect(jitter(1_000)).toBe(1_000);
    expect(jitter(1_000)).toBeCloseTo(1_099.8);
  });
});

describe('pollingInterval', () => {
  it('replaces an interval that is not positive with the default', () => {
    expect([20, 0, -1, Number.NaN, undefined].map((i) => pollingInterval(i, 5_000))).toEqual([20, 5_000, 5_000, 5_000, 5_000]);
  });
});

describe('parseRevision', () => {
  it('accepts positive integers JavaScript represents exactly', () => {
    expect(parseRevision('42')).toBe(42);
    expect(parseRevision(' 7 ')).toBe(7);
    expect(parseRevision(String(Number.MAX_SAFE_INTEGER))).toBe(Number.MAX_SAFE_INTEGER);
    for (const v of [undefined, null, '', '0', '-1', '1.5', '1e3', 'abc', '18446744073709551615']) {
      expect(parseRevision(v)).toBeUndefined();
    }
  });
});

describe('Transport.request', () => {
  it.each([
    ['seconds', '2', 2_000],
    ['a date', new Date(Date.now() + 60_000).toUTCString(), 60_000],
    ['too long a wait', '86400', 5 * 60_000],
    ['nonsense', 'soon', undefined],
  ])('reads Retry-After given as %s', async (_name, header, want) => {
    const realm = await fakeRealm();
    cleanup.push(realm.close);
    realm.intercept((_req, res) => {
      res.writeHead(503, { 'Content-Type': 'application/json', 'Retry-After': header });
      res.end('{"errors":["busy"]}');
      return true;
    });
    const err = await new Transport({ address: realm.address }).request('chambers', { method: 'GET' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(RealmError);
    const retryAfter = (err as RealmError).retryAfter;
    if (want === undefined) expect(retryAfter).toBeUndefined();
    else expect(retryAfter).toBeGreaterThan(want - 2_000); // a date has whole seconds
    if (want !== undefined) expect(retryAfter).toBeLessThanOrEqual(want);
  });
});
