import { apiClient } from '@/api/client';
import type { ExternalRoutingMarker, RoutingRulesData } from '@/types/api';

/**
 * getRoutingRules. The Go router currently registers this as POST
 * (reconciliation #6); a 204 response means external routing is in use.
 * Verify the verb against internal/admin/router.go at integration time.
 */
export function getRoutingRules(): Promise<
  RoutingRulesData[] | ExternalRoutingMarker
> {
  return apiClient.postMaybeNoContent<RoutingRulesData[]>(
    '/webapp/getRoutingRules',
    {},
  );
}

/** POST /webapp/updateRoutingRules — persists a single rule. */
export function updateRoutingRules(
  rule: RoutingRulesData,
): Promise<RoutingRulesData[]> {
  return apiClient.post<RoutingRulesData[]>(
    '/webapp/updateRoutingRules',
    rule,
  );
}
