// Counts the flag values a ServerClient serves as realm.flag.evaluations, on
// an OpenTelemetry meter the application passes in. The Go SDK
// (pkg/evaluations.go) and the server's evaluation endpoint report the same
// counter, with the same attributes.

import { ruleName } from './evaluate.js';
import type { Flag } from './types.js';

/**
 * The part of an OpenTelemetry `Meter` (from `@opentelemetry/api`) that a
 * ServerClient uses, so that this package depends on no OpenTelemetry
 * package: pass `metrics.getMeter(...)`.
 */
export interface Meter {
  createObservableCounter(name: string, options?: { description?: string; unit?: string }): ObservableCounter;
}

/** The part of an OpenTelemetry `ObservableCounter` that a ServerClient uses. */
export interface ObservableCounter {
  addCallback(callback: (result: ObservableResult) => void): void;
}

/** The part of an OpenTelemetry `ObservableResult` that a ServerClient uses. */
export interface ObservableResult {
  observe(value: number, attributes?: Record<string, string>): void;
}

// One series of realm.flag.evaluations.
interface Series {
  readonly attributes: Readonly<Record<'flag' | 'path' | 'rule', string>>;
  count: number;
}

// The series of a flag's default, then of each of its rules, at a path, so
// that the series of rule (servingRule's index, -1 for the default) is at
// rule + 1.
interface FlagSeries {
  readonly path: string;
  readonly key: string;
  readonly series: readonly Series[];
}

/**
 * Counts evaluations by flag, path, and the rule that served them, reported
 * as the observable counter realm.flag.evaluations whenever the meter's
 * provider collects it. A series is kept once counted, so its count only
 * grows: their number is bounded by the paths, flags, and rules served.
 */
export class Evaluations {
  readonly #series = new Map<string, Series>();
  // each flag's series, found on its first evaluation, so that counting one
  // after that is an increment
  readonly #flags = new WeakMap<Flag, FlagSeries>();

  constructor(meter: Meter) {
    meter
      .createObservableCounter('realm.flag.evaluations', {
        description:
          'Flag values served, by flag, path, and the targeting rule that served them (its id, its position such as #2 when it has none, or default).',
        unit: '{evaluation}',
      })
      .addCallback((result) => {
        // as the Go SDK does, a rule not yet served is not reported
        for (const s of this.#series.values()) if (s.count > 0) result.observe(s.count, s.attributes);
      });
  }

  /** Counts an evaluation of flag, held at key in the chamber at path, served by the rule at index rule (-1: its default). */
  count(path: string, key: string, flag: Flag, rule: number): void {
    let found = this.#flags.get(flag);
    if (!found || found.path !== path || found.key !== key) {
      found = { path, key, series: this.#seriesOf(path, key, flag) };
      this.#flags.set(flag, found);
    }
    found.series[rule + 1]!.count++;
  }

  // seriesOf returns the series of flag's default, then of each of its
  // targeting rules, creating those not yet counted.
  #seriesOf(path: string, key: string, flag: Flag): Series[] {
    const rules = Array.isArray(flag.targeting) ? flag.targeting.length : 0;
    const series: Series[] = [];
    for (let i = -1; i < rules; i++) {
      const rule = ruleName(flag, i);
      const id = JSON.stringify([path, key, rule]);
      let s = this.#series.get(id);
      if (!s) {
        s = { attributes: { flag: key, path, rule }, count: 0 };
        this.#series.set(id, s);
      }
      series.push(s);
    }
    return series;
  }
}
