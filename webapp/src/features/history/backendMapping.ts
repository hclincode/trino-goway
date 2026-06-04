import type { BackendData } from '@/types/api';

export interface BackendMapping {
  /** Resolve a backend display name from its internal (proxyTo) or external URL. */
  nameOf: (url: string) => string;
  /**
   * Resolve a usable external URL from a query's backendUrl. Falls back through
   * the backend's externalUrl/proxyTo. Empty string when unresolved
   * (Go gap #4: query records carry a blank externalUrl).
   */
  externalUrlOf: (backendUrl: string) => string;
}

export function buildBackendMapping(backends: BackendData[]): BackendMapping {
  const nameByUrl = new Map<string, string>();
  const externalByAnyUrl = new Map<string, string>();

  for (const b of backends) {
    const external = b.externalUrl ?? '';
    if (b.proxyTo) {
      nameByUrl.set(b.proxyTo, b.name);
      externalByAnyUrl.set(b.proxyTo, external || b.proxyTo);
    }
    if (external) {
      nameByUrl.set(external, b.name);
      externalByAnyUrl.set(external, external);
    }
    nameByUrl.set(b.name, b.name);
  }

  return {
    nameOf: (url) => nameByUrl.get(url) ?? url,
    externalUrlOf: (backendUrl) => externalByAnyUrl.get(backendUrl) ?? '',
  };
}
