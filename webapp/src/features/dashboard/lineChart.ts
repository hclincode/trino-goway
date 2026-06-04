import type { LineChartData } from '@/types/api';
import { formatZonedTimestamp } from '@/utils/time';

export interface LineChartModel {
  /** X-axis category labels (HH:mm in the selected timezone). */
  categories: string[];
  /** One series per backend; values aligned to `categories` (0 for gaps). */
  series: { name: string; data: number[] }[];
}

const MINUTE = 60_000;

/**
 * Bucket per-backend line-chart points into 1-minute slots between the global
 * min and max timestamp, producing aligned series for ECharts. Empty input
 * yields empty categories/series (the chart renders a "no data" state).
 */
export function buildLineChartModel(
  lineChart: Record<string, LineChartData[]>,
  timezone: string,
): LineChartModel {
  const backends = Object.keys(lineChart);
  const allPoints = backends.flatMap((name) => lineChart[name] ?? []);
  if (allPoints.length === 0) {
    return { categories: [], series: [] };
  }

  let min = Infinity;
  let max = -Infinity;
  for (const p of allPoints) {
    if (p.epochMillis < min) min = p.epochMillis;
    if (p.epochMillis > max) max = p.epochMillis;
  }
  const startBucket = Math.floor(min / MINUTE) * MINUTE;
  const endBucket = Math.floor(max / MINUTE) * MINUTE;

  const buckets: number[] = [];
  for (let ts = startBucket; ts <= endBucket; ts += MINUTE) {
    buckets.push(ts);
  }
  const bucketIndex = new Map<number, number>();
  buckets.forEach((ts, i) => bucketIndex.set(ts, i));

  const categories = buckets.map((ts) => formatZonedTimestamp(ts, timezone));

  const series = backends.map((name) => {
    const data = new Array<number>(buckets.length).fill(0);
    for (const p of lineChart[name] ?? []) {
      const slot = Math.floor(p.epochMillis / MINUTE) * MINUTE;
      const idx = bucketIndex.get(slot);
      if (idx !== undefined) {
        data[idx] = (data[idx] ?? 0) + p.queryCount;
      }
    }
    return { name, data };
  });

  return { categories, series };
}
