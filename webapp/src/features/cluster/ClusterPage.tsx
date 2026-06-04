import { useMemo, useState } from 'react';
import {
  Button,
  Popconfirm,
  Space,
  Table,
  type TableColumnsType,
} from 'antd';
import {
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { notify } from '@/app/notify';
import { Role, useAccessStore } from '@/stores/access';
import type { BackendData, ProxyBackend } from '@/types/api';
import { StatusTag } from '@/components/StatusTag';
import { ExternalLink } from '@/components/ExternalLink';
import {
  useBackends,
  useDeleteBackend,
  useSaveBackend,
  useUpdateBackend,
} from './queries';
import { ActiveSwitch } from './ActiveSwitch';
import { BackendModal } from './BackendModal';

export default function ClusterPage() {
  const { t } = useTranslation();
  const isAdmin = useAccessStore((s) => s.hasRole(Role.ADMIN));
  const { data: backends = [], isLoading } = useBackends();
  const saveMutation = useSaveBackend();
  const updateMutation = useUpdateBackend();
  const deleteMutation = useDeleteBackend();

  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<BackendData | undefined>(undefined);

  const routingGroupFilters = useMemo(
    () =>
      Array.from(new Set(backends.map((b) => b.routingGroup)))
        .sort()
        .map((g) => ({ text: g, value: g })),
    [backends],
  );

  const openCreate = () => {
    setEditing(undefined);
    setModalOpen(true);
  };
  const openEdit = (record: BackendData) => {
    setEditing(record);
    setModalOpen(true);
  };

  const onSubmit = async (values: ProxyBackend) => {
    try {
      if (editing) {
        await updateMutation.mutateAsync(values);
        notify.success(t('cluster.updated'));
      } else {
        await saveMutation.mutateAsync(values);
        notify.success(t('cluster.created'));
      }
      setModalOpen(false);
    } catch (err) {
      notify.error(
        err instanceof Error
          ? err.message
          : editing
            ? t('cluster.errorUpdate')
            : t('cluster.errorCreate'),
      );
    }
  };

  const onDelete = async (record: BackendData) => {
    try {
      await deleteMutation.mutateAsync(record.name);
      notify.success(t('cluster.deleted'));
    } catch (err) {
      notify.error(err instanceof Error ? err.message : t('cluster.errorDelete'));
    }
  };

  const columns: TableColumnsType<BackendData> = [
    {
      title: t('cluster.name'),
      dataIndex: 'name',
      sorter: (a, b) => a.name.localeCompare(b.name),
    },
    {
      title: t('cluster.routingGroup'),
      dataIndex: 'routingGroup',
      sorter: (a, b) => a.routingGroup.localeCompare(b.routingGroup),
      filters: routingGroupFilters,
      onFilter: (value, record) => record.routingGroup === value,
    },
    {
      title: t('cluster.proxyTo'),
      dataIndex: 'proxyTo',
      render: (url: string) => <ExternalLink href={url} />,
    },
    {
      title: t('cluster.externalUrl'),
      dataIndex: 'externalUrl',
      render: (url?: string) => <ExternalLink href={url} />,
    },
    {
      title: t('cluster.queued'),
      dataIndex: 'queued',
      sorter: (a, b) => a.queued - b.queued,
    },
    {
      title: t('cluster.running'),
      dataIndex: 'running',
      sorter: (a, b) => a.running - b.running,
    },
    {
      title: t('cluster.active'),
      dataIndex: 'active',
      render: (_: boolean, record) => (
        <ActiveSwitch record={record} disabled={!isAdmin} />
      ),
    },
    {
      title: t('cluster.status'),
      dataIndex: 'status',
      render: (status: string) => <StatusTag status={status} />,
    },
  ];

  if (isAdmin) {
    columns.push({
      title: (
        <Space>
          {t('cluster.operations')}
          <Button
            type="primary"
            size="small"
            icon={<PlusOutlined />}
            onClick={openCreate}
            aria-label={t('ui.create')}
          />
        </Space>
      ),
      key: 'operations',
      render: (_: unknown, record) => (
        <Space>
          <Button
            size="small"
            icon={<EditOutlined />}
            onClick={() => openEdit(record)}
            aria-label={t('ui.edit')}
          />
          <Popconfirm
            title={t('ui.deleteTitle')}
            description={t('ui.deleteContent')}
            okText={t('ui.confirm')}
            cancelText={t('ui.cancel')}
            placement="bottomRight"
            onConfirm={() => onDelete(record)}
          >
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              aria-label={t('ui.delete')}
            />
          </Popconfirm>
        </Space>
      ),
    });
  }

  return (
    <>
      <Table<BackendData>
        rowKey="name"
        columns={columns}
        dataSource={backends}
        loading={isLoading}
        pagination={false}
      />
      <BackendModal
        open={modalOpen}
        editing={editing}
        confirmLoading={saveMutation.isPending || updateMutation.isPending}
        onCancel={() => setModalOpen(false)}
        onSubmit={onSubmit}
      />
    </>
  );
}
