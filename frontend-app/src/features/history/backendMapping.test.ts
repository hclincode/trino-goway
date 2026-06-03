import { describe, expect, it } from 'vitest';
import { buildBackendMapping } from './backendMapping';
import type { BackendData } from '@/types/api';

function backend(p: Partial<BackendData>): BackendData {
  return {
    name: 'b1',
    proxyTo: 'http://internal:8080',
    externalUrl: 'http://external:8080',
    active: true,
    routingGroup: 'adhoc',
    queued: 0,
    running: 0,
    status: 'HEALTHY',
    ...p,
  };
}

describe('buildBackendMapping', () => {
  const mapping = buildBackendMapping([
    backend({
      name: 'alpha',
      proxyTo: 'http://alpha-internal',
      externalUrl: 'http://alpha-external',
    }),
  ]);

  it('resolves a name from the internal (proxyTo) url', () => {
    expect(mapping.nameOf('http://alpha-internal')).toBe('alpha');
  });

  it('resolves a name from the external url', () => {
    expect(mapping.nameOf('http://alpha-external')).toBe('alpha');
  });

  it('falls back to the url itself for an unknown backend', () => {
    expect(mapping.nameOf('http://unknown')).toBe('http://unknown');
  });

  it('resolves an external url from the query backendUrl (Go gap #4)', () => {
    expect(mapping.externalUrlOf('http://alpha-internal')).toBe(
      'http://alpha-external',
    );
  });

  it('returns empty string when the external url cannot be resolved', () => {
    expect(mapping.externalUrlOf('http://nope')).toBe('');
  });

  it('uses proxyTo as the external fallback when externalUrl is blank', () => {
    const m = buildBackendMapping([
      backend({ name: 'beta', proxyTo: 'http://beta-internal', externalUrl: '' }),
    ]);
    expect(m.externalUrlOf('http://beta-internal')).toBe('http://beta-internal');
  });
});
