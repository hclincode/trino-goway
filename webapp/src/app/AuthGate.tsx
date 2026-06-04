import { lazy, Suspense } from 'react';
import { Spin } from 'antd';
import { useAccessStore } from '@/stores/access';
import {
  useConsumeOidcCookie,
  useHydrateUserInfo,
} from '@/features/auth/queries';
import { RootLayout } from '@/layout/RootLayout';

const LoginPage = lazy(() => import('@/features/auth/LoginPage'));

function CenteredSpinner() {
  return (
    <div
      style={{
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'center',
        minHeight: '100vh',
      }}
    >
      <Spin size="large" />
    </div>
  );
}

/**
 * Top-level gate. Consumes an OIDC `token` cookie on mount and hydrates the
 * user profile, then renders the app shell when authenticated or the login
 * screen otherwise.
 */
export function AuthGate() {
  useConsumeOidcCookie();
  useHydrateUserInfo();
  const isAuthorized = useAccessStore((s) => s.isAuthorized());

  if (!isAuthorized) {
    return (
      <Suspense fallback={<CenteredSpinner />}>
        <LoginPage />
      </Suspense>
    );
  }

  return <RootLayout />;
}
