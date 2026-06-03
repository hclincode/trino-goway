import { apiClient } from '@/api/client';
import type { UIConfiguration } from '@/types/api';

/**
 * GET /webapp/getUIConfiguration. `disablePages` may be absent
 * (reconciliation #5) — callers treat absence as "no pages hidden".
 */
export function fetchUIConfiguration(): Promise<UIConfiguration> {
  return apiClient.get<UIConfiguration>('/webapp/getUIConfiguration');
}
