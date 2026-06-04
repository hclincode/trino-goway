import { useQuery } from '@tanstack/react-query';
import { getDistribution } from '@/api/endpoints/dashboard';
import { useAccessStore } from '@/stores/access';

/** POST /webapp/getDistribution — fetched once on mount (no auto-refresh). */
export function useDistribution() {
  const token = useAccessStore((s) => s.token);
  return useQuery({
    queryKey: ['distribution'],
    queryFn: getDistribution,
    enabled: !!token,
  });
}
