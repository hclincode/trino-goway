import { lazy, Suspense } from 'react';
import {
  createBrowserRouter,
  Navigate,
  type RouteObject,
} from 'react-router-dom';
import { AuthGate } from './AuthGate';
import { RouteGuard } from './RouteGuard';
import { PageFallback } from './PageFallback';
import { NAV_ROUTES } from './navConfig';
import { BASENAME } from './constants';

const DashboardPage = lazy(() => import('@/features/dashboard/DashboardPage'));
const ClusterPage = lazy(() => import('@/features/cluster/ClusterPage'));
const HistoryPage = lazy(() => import('@/features/history/HistoryPage'));
const RoutingRulesPage = lazy(
  () => import('@/features/routingRules/RoutingRulesPage'),
);
const HomePage = lazy(() => import('@/features/home/HomePage'));

const PAGE_COMPONENTS: Record<string, React.LazyExoticComponent<React.FC>> = {
  dashboard: DashboardPage,
  cluster: ClusterPage,
  history: HistoryPage,
  'routing-rules': RoutingRulesPage,
};

const pageRoutes: RouteObject[] = NAV_ROUTES.map((route) => {
  const Page = PAGE_COMPONENTS[route.itemKey];
  return {
    path: route.path.replace(/^\//, ''),
    element: (
      <RouteGuard itemKey={route.itemKey} roles={route.roles}>
        <Suspense fallback={<PageFallback />}>{Page ? <Page /> : null}</Suspense>
      </RouteGuard>
    ),
  };
});

export const router = createBrowserRouter(
  [
    {
      path: '/',
      element: <AuthGate />,
      children: [
        { index: true, element: <Navigate to="/dashboard" replace /> },
        ...pageRoutes,
        {
          path: '*',
          element: (
            <Suspense fallback={<PageFallback />}>
              <HomePage />
            </Suspense>
          ),
        },
      ],
    },
  ],
  { basename: BASENAME },
);
