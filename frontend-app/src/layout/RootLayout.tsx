import { useMemo, useState } from 'react';
import { Layout, Menu } from 'antd';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useVisibleRoutes } from '@/hooks/useVisibleRoutes';
import { ThemeToggle } from './ThemeToggle';
import { TimezoneSelect } from './TimezoneSelect';
import { UserMenu } from './UserMenu';
import styles from './RootLayout.module.css';

const { Header, Sider, Content } = Layout;

/** App shell: fixed header + collapsible sidebar + routed content. */
export function RootLayout() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const routes = useVisibleRoutes();
  const [collapsed, setCollapsed] = useState(false);

  const menuItems = useMemo(
    () =>
      routes.map((r) => ({
        key: r.path,
        icon: r.icon,
        label: t(r.labelKey as never),
      })),
    [routes, t],
  );

  // Highlight the nav item matching the current path prefix.
  const selectedKey =
    routes.find((r) => location.pathname.startsWith(r.path))?.path ?? '';

  return (
    <Layout className={styles.layout}>
      <Header className={styles.header}>
        <div className={styles.brand}>
          <img
            src="/trino-gateway/logo.svg"
            alt=""
            className={styles.brandLogo}
          />
          <span>Trino Gateway</span>
        </div>
        <div className={styles.headerRight}>
          <TimezoneSelect />
          <ThemeToggle />
          <UserMenu />
        </div>
      </Header>
      <Layout>
        <Sider
          collapsible
          collapsed={collapsed}
          onCollapse={setCollapsed}
          width={240}
          collapsedWidth={60}
          breakpoint="lg"
        >
          <Menu
            mode="inline"
            selectedKeys={selectedKey ? [selectedKey] : []}
            items={menuItems}
            onClick={({ key }) => navigate(key)}
            style={{ height: '100%', borderInlineEnd: 0 }}
          />
        </Sider>
        <Content className={styles.content}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  );
}
