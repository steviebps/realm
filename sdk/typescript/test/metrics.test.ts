import { DataPointType, MeterProvider, MetricReader } from '@opentelemetry/sdk-metrics';
import { afterEach, describe, expect, it } from 'vitest';
import { ServerClient } from '../src/index.js';
import { fakeRealm } from './fake-realm.js';

// A reader that collects only when a test asks it to.
class TestReader extends MetricReader {
  protected async onShutdown(): Promise<void> {}
  protected async onForceFlush(): Promise<void> {}
}

// evaluations returns realm.flag.evaluations as the reader collects it, as
// "<flag> <path> <rule>" → count.
async function evaluations(reader: TestReader): Promise<Record<string, number>> {
  const { resourceMetrics } = await reader.collect();
  const counts: Record<string, number> = {};
  for (const scope of resourceMetrics.scopeMetrics) {
    for (const metric of scope.metrics) {
      if (metric.descriptor.name !== 'realm.flag.evaluations') continue;
      expect(metric.dataPointType === DataPointType.SUM && metric.isMonotonic).toBe(true);
      for (const point of metric.dataPoints) {
        const { flag, path, rule } = point.attributes;
        counts[`${String(flag)} ${String(path)} ${String(rule)}`] = point.value as number;
      }
    }
  }
  return counts;
}

const checkout = {
  rules: {
    checkout: {
      type: 'boolean',
      value: false,
      targeting: [
        { id: 'beta', clauses: [{ attribute: 'tenantId', op: 'in', values: ['acme'] }], value: true },
        { clauses: [{ attribute: 'tenantId', op: 'in', values: ['globex'] }], value: true },
      ],
    },
    theme: { type: 'string', value: 'light' },
    layout: { type: 'custom', value: { columns: 2 } },
  },
};

let cleanup: (() => unknown)[] = [];
afterEach(async () => {
  for (const fn of cleanup.reverse()) await fn();
  cleanup = [];
});

// setup returns a started client of the chamber checkout at /prod/web/,
// counting on a meter of its own, and the reader of that meter.
async function setup() {
  const realm = await fakeRealm();
  cleanup.push(realm.close);
  realm.publish(checkout);
  const reader = new TestReader();
  const provider = new MeterProvider({ readers: [reader] });
  cleanup.push(() => provider.shutdown());
  const client = new ServerClient({ address: realm.address, path: 'prod/web', meter: provider.getMeter('app'), streaming: false });
  cleanup.push(() => client.stop());
  await client.start();
  return { client, reader };
}

const tenant = (tenantId: string) => ({ attributes: { tenantId } });

describe('ServerClient meter', () => {
  it('counts the rule that serves each value a getter returns', async () => {
    const { client, reader } = await setup();
    for (const t of ['acme', 'acme', 'globex', 'initech']) client.bool('checkout', tenant(t), false);
    client.string('theme', undefined, 'dark');
    client.json('layout', undefined, {});
    // none of these serves the flag's value
    client.string('checkout', tenant('acme'), 'off');
    client.bool('missing', undefined, false);

    expect(await evaluations(reader)).toEqual({
      'checkout /prod/web/ beta': 2,
      'checkout /prod/web/ #2': 1,
      'checkout /prod/web/ default': 1,
      'theme /prod/web/ default': 1,
      'layout /prod/web/ default': 1,
    });
  });

  it('counts nothing for allFlags and initialState, whose values pages get counted where browsers are served', async () => {
    const { client, reader } = await setup();
    expect(client.allFlags(tenant('acme'))).toHaveProperty('checkout.value', true);
    expect(client.initialState(tenant('globex'))).toHaveProperty('flags.checkout.value', true);

    expect(await evaluations(reader)).toEqual({});
  });
});
