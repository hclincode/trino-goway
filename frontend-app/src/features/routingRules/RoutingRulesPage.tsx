import { Typography } from 'antd';
import { useTranslation } from 'react-i18next';

// Phase 1 placeholder; the routing-rules page is implemented in Phase 5.
export default function RoutingRulesPage() {
  const { t } = useTranslation();
  return <Typography.Title level={3}>{t('menu.routingRules')}</Typography.Title>;
}
