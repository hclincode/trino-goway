import { Typography } from 'antd';
import { useTranslation } from 'react-i18next';

// Phase 1 placeholder; the history page is implemented in Phase 4.
export default function HistoryPage() {
  const { t } = useTranslation();
  return <Typography.Title level={3}>{t('menu.historyNav')}</Typography.Title>;
}
