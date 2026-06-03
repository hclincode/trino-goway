import type { ReactNode } from 'react';
import { Navigate } from 'react-router-dom';
import { Role, useAccessStore } from '@/stores/access';

interface Props {
  itemKey: string;
  roles: Role[];
  children: ReactNode;
}

/**
 * Guards a page route: redirects to /dashboard if the user lacks the required
 * role or page permission (covers direct deep-links the sidebar would hide).
 */
export function RouteGuard({ itemKey, roles, children }: Props) {
  const hasRole = useAccessStore((s) => s.hasRole);
  const hasPermission = useAccessStore((s) => s.hasPermission);

  const roleOk = roles.length === 0 || roles.some((r) => hasRole(r));
  const permOk = hasPermission(itemKey);

  if (!roleOk || !permOk) {
    return <Navigate to="/dashboard" replace />;
  }
  return <>{children}</>;
}
