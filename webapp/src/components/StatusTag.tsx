import { Tag } from 'antd';

const COLOR: Record<string, string> = {
  HEALTHY: 'green',
  UNHEALTHY: 'red',
  PENDING: 'gold',
  UNKNOWN: 'default',
};

/** Backend health status as a colored tag. */
export function StatusTag({ status }: { status: string }) {
  return <Tag color={COLOR[status] ?? 'default'}>{status}</Tag>;
}
