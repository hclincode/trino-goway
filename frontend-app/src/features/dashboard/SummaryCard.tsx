import { Card, Descriptions, Tooltip } from 'antd';
import { QuestionCircleOutlined } from '@ant-design/icons';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { DistributionDetail } from '@/types/api';
import { useAccessStore } from '@/stores/access';
import { useTimezone } from '@/context/timezone';
import { formatZonedDateTime } from '@/utils/time';

interface Props {
  detail?: DistributionDetail;
}

function LabelWithTip({ text, tip }: { text: string; tip: string }) {
  return (
    <span>
      {text}{' '}
      <Tooltip title={tip}>
        <QuestionCircleOutlined style={{ color: 'rgba(0,0,0,0.45)' }} />
      </Tooltip>
    </span>
  );
}

/** Dashboard summary: 9 key-value rows of gateway health + throughput. */
export function SummaryCard({ detail }: Props) {
  const { t } = useTranslation();
  const { timezone } = useTimezone();
  const hasPermission = useAccessStore((s) => s.hasPermission);

  // The Backends count links to /cluster only when the user may see that page.
  const canSeeCluster = hasPermission('cluster');

  const backendsValue =
    detail === undefined ? (
      ''
    ) : canSeeCluster ? (
      <Link to="/cluster">{detail.totalBackendCount}</Link>
    ) : (
      detail.totalBackendCount
    );

  return (
    <Card title={t('dashboard.summary')}>
      <Descriptions column={1} size="small">
        <Descriptions.Item label={t('dashboard.startTime')}>
          {detail ? formatZonedDateTime(detail.startTime, timezone) : ''}
        </Descriptions.Item>
        <Descriptions.Item label={t('dashboard.backends')}>
          {backendsValue}
        </Descriptions.Item>
        <Descriptions.Item label={t('dashboard.backendsOnline')}>
          {detail?.onlineBackendCount ?? ''}
        </Descriptions.Item>
        <Descriptions.Item label={t('dashboard.backendsOffline')}>
          {detail?.offlineBackendCount ?? ''}
        </Descriptions.Item>
        <Descriptions.Item label={t('dashboard.backendsHealthy')}>
          {detail?.healthyBackendCount ?? ''}
        </Descriptions.Item>
        <Descriptions.Item label={t('dashboard.backendsUnhealthy')}>
          {detail?.unhealthyBackendCount ?? ''}
        </Descriptions.Item>
        <Descriptions.Item
          label={<LabelWithTip text={t('dashboard.qph')} tip={t('dashboard.qphTip')} />}
        >
          {detail?.totalQueryCount ?? ''}
        </Descriptions.Item>
        <Descriptions.Item
          label={<LabelWithTip text={t('dashboard.qpm')} tip={t('dashboard.qpmTip')} />}
        >
          {detail ? detail.averageQueryCountMinute.toFixed(2) : ''}
        </Descriptions.Item>
        <Descriptions.Item
          label={<LabelWithTip text={t('dashboard.qps')} tip={t('dashboard.qpsTip')} />}
        >
          {detail ? detail.averageQueryCountSecond.toFixed(2) : ''}
        </Descriptions.Item>
      </Descriptions>
    </Card>
  );
}
