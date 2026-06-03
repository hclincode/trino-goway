import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  deleteBackend,
  getAllBackends,
  saveBackend,
  updateBackend,
} from '@/api/endpoints/cluster';
import type { BackendData, ProxyBackend } from '@/types/api';
import { useAccessStore } from '@/stores/access';

const KEY = ['backends'];

/** POST /webapp/getAllBackends, sorted alphabetically by name. */
export function useBackends() {
  const token = useAccessStore((s) => s.token);
  return useQuery({
    queryKey: KEY,
    queryFn: getAllBackends,
    enabled: !!token,
    select: (rows: BackendData[]) =>
      [...rows].sort((a, b) => a.name.localeCompare(b.name)),
  });
}

export function useSaveBackend() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (backend: ProxyBackend) => saveBackend(backend),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export function useUpdateBackend() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (backend: ProxyBackend) => updateBackend(backend),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export function useDeleteBackend() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => deleteBackend(name),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}
