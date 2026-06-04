import { Empty, Spin } from 'antd';
import { useTranslation } from 'react-i18next';
import type { RoutingRulesData } from '@/types/api';
import { isExternalRouting, useRoutingRules } from './queries';
import { RuleCard } from './RuleCard';

export default function RoutingRulesPage() {
  const { t } = useTranslation();
  const { data, isLoading } = useRoutingRules();

  if (isLoading) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 40 }}>
        <Spin size="large" />
      </div>
    );
  }

  if (isExternalRouting(data)) {
    return <Empty description={t('routingRules.external')} />;
  }

  const rules: RoutingRulesData[] = data ?? [];
  if (rules.length === 0) {
    return <Empty description={t('routingRules.empty')} />;
  }

  return (
    <div>
      {rules.map((rule, index) => (
        <RuleCard key={rule.name || index} rule={rule} index={index} />
      ))}
    </div>
  );
}
