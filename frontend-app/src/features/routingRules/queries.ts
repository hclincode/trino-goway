import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  getRoutingRules,
  updateRoutingRules,
} from '@/api/endpoints/routingRules';
import { EXTERNAL_ROUTING } from '@/api/client';
import type { ExternalRoutingMarker, RoutingRulesData } from '@/types/api';
import { useAccessStore } from '@/stores/access';

const KEY = ['routingRules'];

export function isExternalRouting(
  data: RoutingRulesData[] | ExternalRoutingMarker | undefined,
): data is ExternalRoutingMarker {
  return data === EXTERNAL_ROUTING || !!(data && 'isExternalRouting' in data);
}

/** getRoutingRules — resolves to rules[] or the external-routing sentinel. */
export function useRoutingRules() {
  const token = useAccessStore((s) => s.token);
  return useQuery({
    queryKey: KEY,
    queryFn: getRoutingRules,
    enabled: !!token,
  });
}

export function useUpdateRoutingRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (rule: RoutingRulesData) => updateRoutingRules(rule),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}
