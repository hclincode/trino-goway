import { useQuery } from '@tanstack/react-query';
import { fetchUIConfiguration } from '@/api/endpoints/uiConfig';
import { NAV_ROUTES, type NavRoute } from '@/app/navConfig';
import { useAccessStore } from '@/stores/access';

/** GET /webapp/getUIConfiguration (for `disablePages`); tolerant of absence. */
export function useUIConfiguration() {
  const token = useAccessStore((s) => s.token);
  return useQuery({
    queryKey: ['uiConfiguration'],
    queryFn: fetchUIConfiguration,
    enabled: !!token,
    staleTime: Infinity,
    retry: false,
  });
}

/**
 * Routes the current user may see: filtered by role, server permission list,
 * and the optional `disablePages` array. Absent `disablePages` hides nothing
 * (reconciliation #5).
 */
export function useVisibleRoutes(): NavRoute[] {
  const hasRole = useAccessStore((s) => s.hasRole);
  const hasPermission = useAccessStore((s) => s.hasPermission);
  const { data: uiConfig } = useUIConfiguration();
  const disablePages = uiConfig?.disablePages ?? [];

  return NAV_ROUTES.filter((route) => {
    if (route.roles.length > 0 && !route.roles.some((r) => hasRole(r))) {
      return false;
    }
    if (!hasPermission(route.itemKey)) return false;
    if (disablePages.includes(route.itemKey)) return false;
    return true;
  });
}
