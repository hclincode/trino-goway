import type { ReactNode } from 'react';
import {
  DashboardOutlined,
  ClusterOutlined,
  HistoryOutlined,
  ApartmentOutlined,
} from '@ant-design/icons';
import { Role } from '@/stores/access';

export interface NavRoute {
  /** Stable key used for sidebar selection + permission checks. */
  itemKey: string;
  /** Router path (relative to the base path). */
  path: string;
  /** i18n key for the sidebar label. */
  labelKey: string;
  icon: ReactNode;
  /** Empty = all roles allowed. */
  roles: Role[];
}

export const NAV_ROUTES: NavRoute[] = [
  {
    itemKey: 'dashboard',
    path: '/dashboard',
    labelKey: 'menu.dashboard',
    icon: <DashboardOutlined />,
    roles: [],
  },
  {
    itemKey: 'cluster',
    path: '/cluster',
    labelKey: 'menu.cluster',
    icon: <ClusterOutlined />,
    roles: [],
  },
  {
    itemKey: 'history',
    path: '/history',
    labelKey: 'menu.historyNav',
    icon: <HistoryOutlined />,
    roles: [],
  },
  {
    itemKey: 'routing-rules',
    path: '/routing-rules',
    labelKey: 'menu.routingRules',
    icon: <ApartmentOutlined />,
    roles: [],
  },
];
