import { getToken, useAccessStore } from '@/stores/access';
import type { ApiEnvelope, ExternalRoutingMarker } from '@/types/api';

/** Error carrying the gateway's envelope code + message. */
export class ApiError extends Error {
  readonly code: number;
  constructor(code: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
  }
}

/** Raised on 401/403 (HTTP or envelope) so callers can distinguish expiry. */
export class SessionExpiredError extends ApiError {
  constructor(message: string) {
    super(401, message);
    this.name = 'SessionExpiredError';
  }
}

/** Raised when the network/fetch itself fails. */
export class NetworkError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'NetworkError';
  }
}

/** Sentinel value returned for HTTP 204 (external routing in use). */
export const EXTERNAL_ROUTING: ExternalRoutingMarker = { isExternalRouting: true };

/**
 * Session-expiry subscribers. The API client must not import UI, so the React
 * layer registers a handler (toast + redirect to login) here.
 */
type ExpiryHandler = () => void;
const expiryHandlers = new Set<ExpiryHandler>();

export function onSessionExpired(handler: ExpiryHandler): () => void {
  expiryHandlers.add(handler);
  return () => expiryHandlers.delete(handler);
}

function handleSessionExpired(): void {
  // Clear the token first so re-renders see an unauthenticated state.
  useAccessStore.getState().clear();
  for (const handler of expiryHandlers) {
    handler();
  }
}

// Messages live here transiently; replaced by i18n keys at the call sites/UI.
const MSG_NETWORK = 'The network has wandered off, try again later';
const MSG_EXPIRED = 'Your session has expired, please sign in again';

/** Dev uses the Vite proxy prefix; prod calls same-origin under the base path. */
const PROXY_PATH = import.meta.env.VITE_PROXY_PATH ?? '/api';

function buildUrl(path: string, params?: Record<string, string>): string {
  const qs =
    params && Object.keys(params).length > 0
      ? '?' + new URLSearchParams(params).toString()
      : '';
  return `${PROXY_PATH}${path}${qs}`;
}

function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = {
    'x-requested-with': 'XMLHttpRequest',
    'Content-Language': 'en_US',
    ...extra,
  };
  const token = getToken();
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  return headers;
}

async function rawFetch(input: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(input, init);
  } catch {
    throw new NetworkError(MSG_NETWORK);
  }
}

/**
 * Unwrap the {code,msg,data} envelope. On 401/403 (HTTP status or envelope
 * code), trigger session expiry and throw SessionExpiredError.
 */
async function handleResponse<T>(res: Response): Promise<T> {
  if (res.status === 401 || res.status === 403) {
    handleSessionExpired();
    throw new SessionExpiredError(MSG_EXPIRED);
  }
  if (res.status !== 200) {
    // Non-200, non-auth (incl. 5xx): try to surface the server msg.
    let serverMsg = MSG_NETWORK;
    try {
      const body = (await res.json()) as Partial<ApiEnvelope<unknown>>;
      if (body && typeof body.msg === 'string' && body.msg) {
        serverMsg = body.msg;
      }
    } catch {
      // non-JSON error body; fall back to the network message
    }
    throw new ApiError(res.status, serverMsg);
  }

  const envelope = (await res.json()) as ApiEnvelope<T>;
  if (envelope.code === 401 || envelope.code === 403) {
    handleSessionExpired();
    throw new SessionExpiredError(MSG_EXPIRED);
  }
  if (envelope.code !== 200) {
    throw new ApiError(envelope.code, envelope.msg || MSG_NETWORK);
  }
  return envelope.data;
}

/** Typed transport over the gateway's envelope. UI-free by design. */
export const apiClient = {
  async post<T>(path: string, body: unknown = {}): Promise<T> {
    const res = await rawFetch(buildUrl(path), {
      method: 'POST',
      headers: authHeaders({ 'Content-Type': 'application/json' }),
      body: JSON.stringify(body),
    });
    return handleResponse<T>(res);
  },

  async get<T>(path: string, params?: Record<string, string>): Promise<T> {
    const res = await rawFetch(buildUrl(path, params), {
      method: 'GET',
      headers: authHeaders(),
    });
    return handleResponse<T>(res);
  },

  /**
   * GET that may legitimately answer 204 (external routing). Resolves to the
   * EXTERNAL_ROUTING sentinel in that case instead of unwrapping an envelope.
   */
  async getMaybeNoContent<T>(
    path: string,
    params?: Record<string, string>,
  ): Promise<T | ExternalRoutingMarker> {
    const res = await rawFetch(buildUrl(path, params), {
      method: 'GET',
      headers: authHeaders(),
    });
    if (res.status === 204) {
      return EXTERNAL_ROUTING;
    }
    return handleResponse<T>(res);
  },

  /**
   * POST variant of the 204-tolerant call. The Go router currently registers
   * getRoutingRules as POST (reconciliation #6); this keeps the 204 semantics.
   */
  async postMaybeNoContent<T>(
    path: string,
    body: unknown = {},
  ): Promise<T | ExternalRoutingMarker> {
    const res = await rawFetch(buildUrl(path), {
      method: 'POST',
      headers: authHeaders({ 'Content-Type': 'application/json' }),
      body: JSON.stringify(body),
    });
    if (res.status === 204) {
      return EXTERNAL_ROUTING;
    }
    return handleResponse<T>(res);
  },
};
