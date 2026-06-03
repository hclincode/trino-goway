import { apiClient } from '@/api/client';
import type { DistributionDetail } from '@/types/api';

/** POST /webapp/getDistribution. */
export function getDistribution(): Promise<DistributionDetail> {
  return apiClient.post<DistributionDetail>('/webapp/getDistribution', {});
}
