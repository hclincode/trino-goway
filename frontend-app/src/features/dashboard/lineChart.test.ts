import { describe, expect, it } from 'vitest';
import { buildLineChartModel } from './lineChart';
import type { LineChartData } from '@/types/api';

function pt(epochMillis: number, name: string, queryCount: number): LineChartData {
  return { epochMillis, name, queryCount, backendUrl: name };
}

describe('buildLineChartModel', () => {
  it('returns empty model for empty input (graceful degradation)', () => {
    const model = buildLineChartModel({}, 'UTC');
    expect(model.categories).toEqual([]);
    expect(model.series).toEqual([]);
  });

  it('buckets points by minute and aligns series across backends', () => {
    const t0 = Date.UTC(2026, 0, 1, 10, 0, 0);
    const t1 = t0 + 60_000;
    const t2 = t0 + 120_000;
    const model = buildLineChartModel(
      {
        a: [pt(t0, 'a', 2), pt(t2, 'a', 5)],
        b: [pt(t1, 'b', 3)],
      },
      'UTC',
    );

    // Three 1-minute buckets spanning t0..t2.
    expect(model.categories).toHaveLength(3);
    const a = model.series.find((s) => s.name === 'a');
    const b = model.series.find((s) => s.name === 'b');
    expect(a?.data).toEqual([2, 0, 5]);
    expect(b?.data).toEqual([0, 3, 0]);
  });

  it('sums multiple points landing in the same minute bucket', () => {
    const t0 = Date.UTC(2026, 0, 1, 10, 0, 0);
    const model = buildLineChartModel(
      { a: [pt(t0, 'a', 1), pt(t0 + 5_000, 'a', 4)] },
      'UTC',
    );
    expect(model.series[0]?.data).toEqual([5]);
  });
});
