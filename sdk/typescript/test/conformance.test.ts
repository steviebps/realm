import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { bucket, clauseMatches, evaluateChamber, ruleName, servingRule } from '../src/evaluate.js';
import type { Chamber, EvaluationContext, Operator } from '../src/types.js';

// The cases the Go SDK runs too (pkg/conformance_test.go).
const conformance = JSON.parse(readFileSync(new URL('../../conformance/evaluation.json', import.meta.url), 'utf8')) as {
  buckets: { seed: string; key: string; bucket: number }[];
  semver: { op: Operator; bound: string; version: string; match: boolean }[];
  cases: {
    name: string;
    defaults?: Record<string, string>;
    chamber: Chamber;
    contexts: {
      now?: string;
      context: EvaluationContext;
      expect: Record<string, unknown>;
      // the rule that serves each flag, when present
      rules?: Record<string, string>;
    }[];
  }[];
};

describe('conformance', () => {
  it.each(conformance.buckets.map((b) => [b.seed, b.key.slice(0, 20), b] as const))('buckets %j %j', (_s, _k, b) => {
    expect(bucket(b.seed, b.key)).toBe(b.bucket);
  });

  it.each(conformance.semver.map((s) => [s.version, s.op, s.bound, s] as const))('%j %s %j', (_v, _o, _b, s) => {
    const clause = { attribute: 'version', op: s.op, values: [s.bound] };
    expect(clauseMatches(clause, { attributes: { version: s.version } })).toBe(s.match);
  });

  describe.each(conformance.cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    it.each(c.contexts.map((cc, i) => [i, cc] as const))('context %i', (_i, cc) => {
      const values = Object.fromEntries(
        Object.entries(
          evaluateChamber(c.chamber, cc.context, c.defaults, cc.now === undefined ? undefined : Date.parse(cc.now)),
        ).map(([k, f]) => [k, f.value]),
      );
      expect(values).toEqual(cc.expect);

      const now = cc.now === undefined ? undefined : Date.parse(cc.now);
      const rules = Object.fromEntries(
        Object.keys(cc.rules ?? {}).map((key) => {
          const flag = c.chamber.rules[key]!;
          return [key, ruleName(flag, servingRule(key, flag, cc.context, c.defaults, now))];
        }),
      );
      expect(rules).toEqual(cc.rules ?? {});
    });
  });
});
