import { useMemo } from 'react';
import { Card, Empty } from 'antd';
import ReactECharts from 'echarts-for-react';
import type { EChartsOption } from 'echarts';
import { useTranslation } from 'react-i18next';
import type { DistributionDetail } from '@/types/api';
import { useChartColors } from '@/hooks/useChartColors';

interface Props {
  detail?: DistributionDetail;
}

/** Per-backend share of queries (doughnut chart). */
export function DoughnutChartCard({ detail }: Props) {
  const { t } = useTranslation();
  const colors = useChartColors();
  const data = useMemo(
    () => detail?.distributionChart ?? [],
    [detail?.distributionChart],
  );

  const option = useMemo<EChartsOption>(
    () => ({
      tooltip: { trigger: 'item' },
      legend: { textStyle: { color: colors.text } },
      series: [
        {
          type: 'pie',
          radius: ['40%', '70%'],
          itemStyle: { borderRadius: 10, borderWidth: 2, borderColor: '#fff' },
          label: { show: false },
          labelLine: { show: false },
          emphasis: {
            label: { show: true, fontSize: 17, fontWeight: 'bold' },
          },
          data: data.map((d) => ({ name: d.name, value: d.queryCount })),
        },
      ],
    }),
    [data, colors],
  );

  return (
    <Card title={t('dashboard.queryDistribution')}>
      {data.length > 0 ? (
        <ReactECharts
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
