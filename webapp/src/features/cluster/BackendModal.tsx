import { useEffect } from 'react';
import { Controller, useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Form, Input, Modal, Switch } from 'antd';
import { useTranslation } from 'react-i18next';
import type { BackendData, ProxyBackend } from '@/types/api';
import { backendFormSchema, type BackendFormValues } from './schema';

interface Props {
  open: boolean;
  /** Present = edit mode (Name disabled); absent = create. */
  editing?: BackendData;
  confirmLoading: boolean;
  onCancel: () => void;
  onSubmit: (values: ProxyBackend) => void;
}

const EMPTY: BackendFormValues = {
  name: '',
  routingGroup: '',
  proxyTo: '',
  externalUrl: '',
  active: false,
};

/** Create / edit backend dialog (react-hook-form + zod). */
export function BackendModal({
  open,
  editing,
  confirmLoading,
  onCancel,
  onSubmit,
}: Props) {
  const { t } = useTranslation();
  const isEdit = !!editing;

  const {
    control,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<BackendFormValues>({
    resolver: zodResolver(backendFormSchema),
    defaultValues: EMPTY,
  });

  // Sync form values when the modal opens for create vs a specific backend.
  useEffect(() => {
    if (!open) return;
    reset(
      editing
        ? {
            name: editing.name,
            routingGroup: editing.routingGroup,
            proxyTo: editing.proxyTo,
            externalUrl: editing.externalUrl ?? '',
            active: editing.active,
          }
        : EMPTY,
    );
  }, [open, editing, reset]);

  const submit = handleSubmit((values) => onSubmit(values));

  return (
    <Modal
      open={open}
      title={isEdit ? t('ui.edit') : t('ui.create')}
      okText={isEdit ? t('ui.save') : t('ui.create')}
      cancelText={t('ui.cancel')}
      confirmLoading={confirmLoading}
      onCancel={onCancel}
      onOk={submit}
      width={500}
      centered
      destroyOnClose
    >
      <Form layout="vertical">
        <Form.Item
          label={t('cluster.name')}
          required
          validateStatus={errors.name ? 'error' : ''}
          help={errors.name ? t('ui.required') : undefined}
        >
          <Controller
            name="name"
            control={control}
            render={({ field }) => <Input {...field} disabled={isEdit} />}
          />
        </Form.Item>
        <Form.Item
          label={t('cluster.routingGroup')}
          required
          validateStatus={errors.routingGroup ? 'error' : ''}
          help={errors.routingGroup ? t('ui.required') : undefined}
        >
          <Controller
            name="routingGroup"
            control={control}
            render={({ field }) => <Input {...field} />}
          />
        </Form.Item>
        <Form.Item
          label={t('cluster.proxyToField')}
          required
          validateStatus={errors.proxyTo ? 'error' : ''}
          help={errors.proxyTo ? t('ui.required') : undefined}
        >
          <Controller
            name="proxyTo"
            control={control}
            render={({ field }) => <Input {...field} />}
          />
        </Form.Item>
        <Form.Item
          label={t('cluster.externalUrl')}
          required
          validateStatus={errors.externalUrl ? 'error' : ''}
          help={errors.externalUrl ? t('ui.required') : undefined}
        >
          <Controller
            name="externalUrl"
            control={control}
            render={({ field }) => <Input {...field} />}
          />
        </Form.Item>
        <Form.Item label={t('cluster.active')}>
          <Controller
            name="active"
            control={control}
            render={({ field }) => (
              <Switch checked={field.value} onChange={field.onChange} />
            )}
          />
        </Form.Item>
      </Form>
    </Modal>
  );
}
