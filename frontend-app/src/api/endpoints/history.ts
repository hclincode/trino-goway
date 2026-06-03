import { apiClient } from '@/api/client';
import type {
  FindQueryHistoryRequest,
  QueryDetail,
  TableData,
} from '@/types/api';

/**
 * POST /webapp/findQueryHistory. Request field names follow the Go contract
 * (reconciliation #1-3): userName / backendUrl / pageSize.
 */
export function findQueryHistory(
  req: FindQueryHistoryRequest,
): Promise<TableData<QueryDetail>> {
  return apiClient.post<TableData<QueryDetail>>(
    '/webapp/findQueryHistory',
    req,
  );
}
