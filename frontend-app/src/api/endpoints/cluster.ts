import { apiClient } from '@/api/client';
import type { BackendData, ProxyBackend } from '@/types/api';

/** POST /webapp/getAllBackends. */
export function getAllBackends(): Promise<BackendData[]> {
  return apiClient.post<BackendData[]>('/webapp/getAllBackends', {});
}

/** POST /webapp/saveBackend (create). */
export function saveBackend(backend: ProxyBackend): Promise<ProxyBackend> {
  return apiClient.post<ProxyBackend>('/webapp/saveBackend', backend);
}

/** POST /webapp/updateBackend (edit / active toggle). */
export function updateBackend(backend: ProxyBackend): Promise<ProxyBackend> {
  return apiClient.post<ProxyBackend>('/webapp/updateBackend', backend);
}

/** POST /webapp/deleteBackend — only `name` is used server-side. */
export function deleteBackend(name: string): Promise<boolean> {
  return apiClient.post<boolean>('/webapp/deleteBackend', { name });
}
