import { Typography } from 'antd';
import { useTranslation } from 'react-i18next';

// Phase 1 placeholder; the dashboard is implemented in Phase 2.
export default function DashboardPage() {
  const { t } = useTranslation();
  return <Typography.Title level={3}>{t('menu.dashboard')}</Typography.Title>;
}
