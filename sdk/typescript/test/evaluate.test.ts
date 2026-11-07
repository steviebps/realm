import { describe, expect, it } from 'vitest';
import { attribute, copyValue, evaluateChamber, normalizeContext } from '../src/evaluate.js';
import type { Chamber, EvaluationContext } from '../src/types.js';

// as a JavaScript caller might pass it, with values that are not strings
const loose = (context: Record<string, unknown>) => context as unknown as EvaluationContext;

describe('evaluateChamber', () => {
  it('leaves out flags of unknown types, and flags too malformed to evaluate', () => {
    const chamber = {
      rules: {
        known: { type: 'string', value: 'served' },
        future: { type: 'futureType', value: { variant: 2 } },
        untyped: { value: true },
        targeting: { type: 'boolean', value: false, targeting: 5 },
        clauses: { type: 'boolean', value: false, targeting: [{ value: true, clauses: { attribute: 'key' } }] },
        primitive: 'x',
        deleted: null,
      },
    } as unknown as Chamber;
    expect(evaluateChamber(chamber, { key: 'u1' })).toEqual({ known: { type: 'string', value: 'served' } });
  });
});

describe('attribute', () => {
  it('reads numbers and booleans as strings, and other values as missing', () => {
    const context = loose({ key: 42, attributes: { n: 7, b: false, o: { x: 1 }, s: 's', nil: null } });
    const defaults = { o: 'default', nil: 'default' };
    expect(['key', 'n', 'b', 'o', 's', 'nil', 'none'].map((name) => attribute(context, defaults, name))).toEqual([
      '42',
      '7',
      'false',
      'default',
      's',
      'default',
      undefined,
    ]);
    expect(attribute(loose({ key: 0 }), undefined, 'key')).toBe('0');
    expect(attribute(loose({ key: {}, attributes: { key: 'k' } }), undefined, 'key')).toBe('k');
  });
});

describe('normalizeContext', () => {
  it('returns a context of strings as it is', () => {
    const context = { key: 'u1', attributes: { tenantId: 't1' } };
    expect(normalizeContext(context)).toBe(context);
  });

  it('turns numbers and booleans into strings and leaves out other values', () => {
    expect(normalizeContext(loose({ key: 42, attributes: { n: 1.5, b: true, o: {}, a: ['x'], nil: null, s: 's' } }))).toEqual({
      key: '42',
      attributes: { n: '1.5', b: 'true', s: 's' },
    });
    expect(normalizeContext(loose({ key: { id: 1 }, attributes: ['x'] }))).toEqual({});
    expect(normalizeContext(null)).toEqual({});
  });
});

describe('copyValue', () => {
  it('copies objects deeply and returns primitives as they are', () => {
    const value = { a: [1, { b: 2 }] };
    const copy = copyValue(value);
    expect(copy).toEqual(value);
    expect(copy).not.toBe(value);
    expect(copy.a[1]).not.toBe(value.a[1]);
    expect(copyValue('s')).toBe('s');
    expect(copyValue(null)).toBeNull();
  });

  it('returns a value it cannot copy as it is', () => {
    const value = { fn: () => 1 };
    expect(copyValue(value)).toBe(value);
  });
});
