import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { findQueryHistory } from '@/api/endpoints/history';
import type { FindQueryHistoryRequest } from '@/types/api';
import { useAccessStore } from '@/stores/access';

/**
 * POST /webapp/findQueryHistory. Field names follow the Go contract
 * (userName/backendUrl/pageSize). Keeps previous page data during refetch for
 * smooth server-side pagination.
 */
export function useQueryHistory(req: FindQueryHistoryRequest) {
  const token = useAccessStore((s) => s.token);
  return useQuery({
    queryKey: ['queryHistory', req],
    queryFn: () => findQueryHistory(req),
    enabled: !!token,
    placeholderData: keepPreviousData,
  });
}
