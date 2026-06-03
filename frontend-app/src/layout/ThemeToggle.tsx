import { Button, Tooltip } from 'antd';
import {
  BulbOutlined,
  MoonOutlined,
  SunOutlined,
} from '@ant-design/icons';
import { Theme } from '@/stores/config';
import { useTheme } from '@/hooks/useTheme';

const ICONS: Record<Theme, React.ReactNode> = {
  [Theme.Auto]: <BulbOutlined />,
  [Theme.Light]: <SunOutlined />,
  [Theme.Dark]: <MoonOutlined />,
};

const LABELS: Record<Theme, string> = {
  [Theme.Auto]: 'Theme: auto',
  [Theme.Light]: 'Theme: light',
  [Theme.Dark]: 'Theme: dark',
};

/** Cycles theme auto -> light -> dark. */
export function ThemeToggle() {
  const { theme, cycle } = useTheme();
  return (
    <Tooltip title={LABELS[theme]}>
      <Button
        type="text"
        aria-label={LABELS[theme]}
        icon={ICONS[theme]}
        onClick={cycle}
      />
    </Tooltip>
  );
}
