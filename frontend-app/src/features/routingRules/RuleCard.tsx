import { useEffect, useState } from 'react';
import { Button, Card, Form, Input } from 'antd';
import { useTranslation } from 'react-i18next';
import { notify } from '@/app/notify';
import { Role, useAccessStore } from '@/stores/access';
import type { RoutingRulesData } from '@/types/api';
import { useUpdateRoutingRule } from './queries';

interface Props {
  rule: RoutingRulesData;
  index: number;
}

interface FormShape {
  description: string;
  priority: string;
  condition: string;
  actions: string;
}

/** Single routing-rule card with its own independent edit/save state. */
export function RuleCard({ rule, index }: Props) {
  const { t } = useTranslation();
  const isAdmin = useAccessStore((s) => s.hasRole(Role.ADMIN));
  const updateMutation = useUpdateRoutingRule();
  const [editing, setEditing] = useState(false);
  const [form] = Form.useForm<FormShape>();

  // Reset the form to the current rule whenever it changes or edit (re)starts.
  useEffect(() => {
    form.setFieldsValue({
      description: rule.description,
      priority: String(rule.priority),
      condition: rule.condition,
      actions: rule.actions.join(', '),
    });
  }, [rule, editing, form]);

  const onSave = async () => {
    const values = await form.validateFields();
    const updated: RoutingRulesData = {
      ...rule,
      description: values.description,
      priority: Number(values.priority),
      condition: values.condition,
      actions: values.actions
        .split(',')
        .map((a) => a.trim())
        .filter((a) => a.length > 0),
    };
    try {
      await updateMutation.mutateAsync(updated);
      setEditing(false);
      notify.success(t('routingRules.updated'));
    } catch (err) {
      notify.error(
        err instanceof Error ? err.message : t('routingRules.errorUpdate'),
      );
    }
  };

  return (
    <Card
      title={t('routingRules.cardTitle', { n: index + 1 })}
      style={{ maxWidth: 800, marginBottom: 20 }}
      extra={
        isAdmin && !editing ? (
          <Button onClick={() => setEditing(true)}>{t('ui.edit')}</Button>
        ) : undefined
      }
      actions={
        isAdmin && editing
          ? [
              <Button
                key="save"
                type="primary"
                loading={updateMutation.isPending}
                onClick={onSave}
              >
                {t('ui.save')}
              </Button>,
            ]
          : undefined
      }
    >
      <Form form={form} layout="vertical">
        <Form.Item label={t('cluster.name')}>
          <Input value={rule.name} disabled />
        </Form.Item>
        <Form.Item name="description" label={t('routingRules.description')}>
          <Input disabled={!editing} />
        </Form.Item>
        <Form.Item name="priority" label={t('routingRules.priority')}>
          <Input disabled={!editing} />
        </Form.Item>
        <Form.Item
          name="condition"
          label={t('routingRules.condition')}
          rules={[{ required: true, message: t('ui.required') }]}
        >
          <Input disabled={!editing} />
        </Form.Item>
        <Form.Item name="actions" label={t('routingRules.actions')}>
          <Input.TextArea autoSize disabled={!editing} />
        </Form.Item>
      </Form>
    </Card>
  );
}
