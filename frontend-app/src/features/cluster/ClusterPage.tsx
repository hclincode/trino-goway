import { Typography } from 'antd';
import { useTranslation } from 'react-i18next';

// Phase 1 placeholder; the cluster page is implemented in Phase 3.
export default function ClusterPage() {
  const { t } = useTranslation();
  return <Typography.Title level={3}>{t('menu.cluster')}</Typography.Title>;
}
