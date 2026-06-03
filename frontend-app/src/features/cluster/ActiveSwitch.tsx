import { useState } from 'react';
import { Switch } from 'antd';
import { useTranslation } from 'react-i18next';
import { notify } from '@/app/notify';
import type { BackendData, ProxyBackend } from '@/types/api';
import { useUpdateBackend } from './queries';

interface Props {
  record: BackendData;
  disabled: boolean;
}

/** Active toggle: flips `active` via updateBackend with a per-row loading state. */
export function ActiveSwitch({ record, disabled }: Props) {
  const { t } = useTranslation();
  const updateMutation = useUpdateBackend();
  const [loading, setLoading] = useState(false);

  const onChange = async (checked: boolean) => {
    const payload: ProxyBackend = {
      name: record.name,
      routingGroup: record.routingGroup,
      proxyTo: record.proxyTo,
      externalUrl: record.externalUrl ?? '',
      active: checked,
    };
    setLoading(true);
    try {
      await updateMutation.mutateAsync(payload);
      notify.success(t('cluster.updated'));
    } catch (err) {
      notify.error(err instanceof Error ? err.message : t('cluster.errorUpdate'));
    } finally {
      setLoading(false);
    }
  };

  return (
    <Switch
      checked={record.active}
      disabled={disabled}
      loading={loading}
      onChange={onChange}
    />
  );
}
