import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ApiError,
  EXTERNAL_ROUTING,
  NetworkError,
  SessionExpiredError,
  apiClient,
  onSessionExpired,
} from './client';
import { useAccessStore } from '@/stores/access';

function mockFetch(opts: { status?: number; json?: unknown }) {
  const res = {
    status: opts.status ?? 200,
    json: async () => opts.json,
  } as unknown as Response;
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(res));
}

describe('apiClient', () => {
  beforeEach(() => {
    useAccessStore.setState({ token: 'tok' });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    useAccessStore.getState().clear();
  });

  it('unwraps a successful envelope to data', async () => {
    mockFetch({ status: 200, json: { code: 200, msg: 'ok', data: { x: 1 } } });
    const data = await apiClient.post<{ x: number }>('/thing');
    expect(data).toEqual({ x: 1 });
  });

  it('sends the bearer token header', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      json: async () => ({ code: 200, msg: 'ok', data: null }),
    } as unknown as Response);
    vi.stubGlobal('fetch', fetchMock);

    await apiClient.post('/thing');

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers.Authorization).toBe('Bearer tok');
    expect(headers['Content-Language']).toBe('en_US');
  });

  it('throws ApiError on a non-200 envelope code', async () => {
    mockFetch({ status: 200, json: { code: 500, msg: 'boom', data: null } });
    await expect(apiClient.post('/thing')).rejects.toBeInstanceOf(ApiError);
    await expect(apiClient.post('/thing')).rejects.toThrow('boom');
  });

  it('triggers session expiry on HTTP 401 and clears the token', async () => {
    const handler = vi.fn();
    const off = onSessionExpired(handler);
    mockFetch({ status: 401, json: {} });

    await expect(apiClient.post('/thing')).rejects.toBeInstanceOf(
      SessionExpiredError,
    );
    expect(handler).toHaveBeenCalledTimes(1);
    expect(useAccessStore.getState().token).toBe('');
    off();
  });

  it('triggers session expiry on a 403 envelope code', async () => {
    const handler = vi.fn();
    const off = onSessionExpired(handler);
    mockFetch({ status: 200, json: { code: 403, msg: 'forbidden', data: null } });

    await expect(apiClient.get('/thing')).rejects.toBeInstanceOf(
      SessionExpiredError,
    );
    expect(handler).toHaveBeenCalled();
    off();
  });

  it('resolves the external-routing sentinel on HTTP 204', async () => {
    mockFetch({ status: 204 });
    const result = await apiClient.postMaybeNoContent('/webapp/getRoutingRules');
    expect(result).toBe(EXTERNAL_ROUTING);
  });

  it('wraps a fetch failure in NetworkError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')));
    await expect(apiClient.post('/thing')).rejects.toBeInstanceOf(NetworkError);
  });
});
