import { useMemo } from 'react';
import { Card, Empty } from 'antd';
import ReactECharts from 'echarts-for-react';
import type { EChartsOption } from 'echarts';
import { useTranslation } from 'react-i18next';
import type { DistributionDetail } from '@/types/api';
import { useTimezone } from '@/context/timezoneContext';
import { useChartColors } from '@/hooks/useChartColors';
import { buildLineChartModel } from './lineChart';

interface Props {
  detail?: DistributionDetail;
}

/** Per-backend per-minute query distribution (line chart). */
export function LineChartCard({ detail }: Props) {
  const { t } = useTranslation();
  const { timezone } = useTimezone();
  const colors = useChartColors();

  const model = useMemo(
    () => buildLineChartModel(detail?.lineChart ?? {}, timezone),
    [detail?.lineChart, timezone],
  );

  const option = useMemo<EChartsOption>(
    () => ({
      tooltip: { trigger: 'axis' },
      legend: { textStyle: { color: colors.text } },
      grid: { left: 48, right: 24, top: 48, bottom: 32 },
      xAxis: {
        type: 'category',
        data: model.categories,
        axisLine: { lineStyle: { color: colors.axisLine } },
        axisLabel: { color: colors.text },
      },
      yAxis: {
        type: 'value',
        minInterval: 1,
        axisLabel: { color: colors.text },
        splitLine: { lineStyle: { color: colors.axisLine } },
      },
      series: model.series.map((s) => ({
        name: s.name,
        type: 'line',
        smooth: true,
        data: s.data,
      })),
    }),
    [model, colors],
  );

  const hasData = model.series.length > 0;

  return (
    <Card title={t('dashboard.queryDistribution')}>
      {hasData ? (
        <ReactECharts
          // Re-key on theme so legend/axis colors refresh on switch.
          key={colors.mode}
          option={option}
          style={{ height: 400 }}
          notMerge
        />
      ) : (
        <Empty
          description={t('dashboard.noLineData')}
          style={{ height: 400, display: 'flex', flexDirection: 'column', justifyContent: 'center' }}
        />
      )}
    </Card>
  );
}
