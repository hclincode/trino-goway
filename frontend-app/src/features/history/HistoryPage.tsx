import { useMemo, useState } from 'react';
import {
  Button,
  Card,
  Form,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  type TableColumnsType,
} from 'antd';
import { useTranslation } from 'react-i18next';
import { Role, useAccessStore } from '@/stores/access';
import type { QueryDetail } from '@/types/api';
import { useTimezone } from '@/context/timezoneContext';
import { formatTimestamp } from '@/utils/time';
import { ExternalLink } from '@/components/ExternalLink';
import { useBackends } from '@/features/cluster/queries';
import { buildBackendMapping } from './backendMapping';
import { useQueryHistory } from './queries';
import { SqlModal } from './SqlModal';

const PAGE_SIZE = 15;
const USER_KEY = 'username';

interface Filters {
  backendUrl: string;
  userName: string;
  queryId: string;
  source: string;
}

function initialUser(fallback: string): string {
  try {
    const stored = sessionStorage.getItem(USER_KEY);
    if (stored) return (JSON.parse(stored) as { user?: string }).user ?? fallback;
  } catch {
    // ignore malformed/unavailable storage
  }
  return fallback;
}

export default function HistoryPage() {
  const { t } = useTranslation();
  const { timezone } = useTimezone();
  const isAdmin = useAccessStore((s) => s.hasRole(Role.ADMIN));
  const userName = useAccessStore((s) => s.userName);

  const { data: backends = [] } = useBackends();
  const mapping = useMemo(() => buildBackendMapping(backends), [backends]);

  const [page, setPage] = useState(1);
  const [filters, setFilters] = useState<Filters>(() => ({
    backendUrl: '',
    userName: initialUser(userName),
    queryId: '',
    source: '',
  }));

  const { data, isFetching } = useQueryHistory({
    page,
    pageSize: PAGE_SIZE,
    backendUrl: filters.backendUrl,
    userName: filters.userName,
    queryId: filters.queryId,
    source: filters.source,
  });

  const [form] = Form.useForm<Filters>();
  const [sqlOpen, setSqlOpen] = useState(false);
  const [sqlText, setSqlText] = useState('');

  const onQuery = (values: Filters) => {
    const next: Filters = {
      backendUrl: values.backendUrl ?? '',
      userName: values.userName ?? '',
      queryId: values.queryId ?? '',
      source: values.source ?? '',
    };
    setFilters(next);
    setPage(1); // filters always refetch from page 1
    try {
      sessionStorage.setItem(USER_KEY, JSON.stringify({ user: next.userName }));
    } catch {
      // ignore unavailable storage
    }
  };

  const openSql = (text: string) => {
    setSqlText(text);
    setSqlOpen(true);
  };

  const routingGroupFilters = useMemo(
    () =>
      Array.from(new Set(backends.map((b) => b.routingGroup)))
        .sort()
        .map((g) => ({ text: g, value: g })),
    [backends],
  );

  const backendOptions = useMemo(
    () =>
      backends.map((b) => ({
        value: b.externalUrl ?? '',
        label: (
          <span>
            <Tag color="blue">{b.name}</Tag>
            {b.externalUrl ?? ''}
          </span>
        ),
      })),
    [backends],
  );

  const columns: TableColumnsType<QueryDetail> = [
    {
      title: t('history.queryId'),
      dataIndex: 'queryId',
      render: (queryId: string, record) => {
        const external =
          record.externalUrl || mapping.externalUrlOf(record.backendUrl);
        return external ? (
          <ExternalLink
            href={`${external}/ui/query.html?${queryId}`}
            text={queryId}
          />
        ) : (
          <span>{queryId}</span>
        );
      },
    },
    {
      title: t('cluster.routingGroup'),
      dataIndex: 'routingGroup',
      sorter: (a, b) => a.routingGroup.localeCompare(b.routingGroup),
      filters: routingGroupFilters,
      onFilter: (value, record) => record.routingGroup === value,
    },
    {
      title: t('history.name'),
      dataIndex: 'backendUrl',
      render: (backendUrl: string) => mapping.nameOf(backendUrl),
    },
    {
      title: t('history.routedTo'),
      dataIndex: 'externalUrl',
      render: (external: string, record) => (
        <ExternalLink href={external || mapping.externalUrlOf(record.backendUrl)} />
      ),
    },
    { title: t('history.user'), dataIndex: 'user' },
    { title: t('history.source'), dataIndex: 'source' },
    {
      title: t('history.queryText'),
      dataIndex: 'queryText',
      width: 300,
      render: (text: string) => (
        <Typography.Link
          ellipsis
          style={{ width: 300, display: 'inline-block' }}
          onClick={() => openSql(text)}
        >
          {text}
        </Typography.Link>
      ),
    },
    {
      title: t('history.submissionTime'),
      dataIndex: 'captureTime',
      sorter: (a, b) => a.captureTime - b.captureTime,
      render: (captureTime: number) => formatTimestamp(captureTime, timezone),
    },
  ];

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Card>
        <Form
          form={form}
          layout="inline"
          initialValues={filters}
          onFinish={onQuery}
        >
          <Form.Item name="backendUrl" label={t('history.routedTo')}>
            <Select
              allowClear
              style={{ width: 220 }}
              placeholder={t('history.routedToTip')}
              options={backendOptions}
            />
          </Form.Item>
          <Form.Item name="userName" label={t('history.user')}>
            <Input allowClear disabled={!isAdmin} style={{ width: 150 }} />
          </Form.Item>
          <Form.Item name="queryId" label={t('history.queryId')}>
            <Input
              allowClear
              style={{ width: 240 }}
              placeholder={t('history.queryIdTip')}
            />
          </Form.Item>
          <Form.Item name="source" label={t('history.source')}>
            <Input allowClear style={{ width: 150 }} />
          </Form.Item>
          <Form.Item>
            <Button htmlType="submit" type="primary">
              {t('ui.query')}
            </Button>
          </Form.Item>
        </Form>
      </Card>

      <Card>
        <Table<QueryDetail>
          rowKey={(r) => `${r.queryId}-${r.captureTime}`}
          columns={columns}
          dataSource={data?.rows ?? []}
          loading={isFetching}
          pagination={{
            current: page,
            pageSize: PAGE_SIZE,
            total: data?.total ?? 0,
            showSizeChanger: false,
            onChange: setPage,
          }}
        />
      </Card>

      <SqlModal open={sqlOpen} sqlText={sqlText} onClose={() => setSqlOpen(false)} />
    </Space>
  );
}
