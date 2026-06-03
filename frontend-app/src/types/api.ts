/**
 * Shared API types mirroring the Go gateway's JSON contract.
 * Field names match the wire format exactly (see docs/studies/webapp-api-and-data-model.md).
 */

/** Standard response envelope for all /webapp/* and /login* endpoints. */
export interface ApiEnvelope<T> {
  code: number;
  msg: string;
  data: T;
}

/** Login mode returned by POST /loginType. */
export type LoginType = 'form' | 'oauth' | 'none';

/** POST /login response payload. */
export interface LoginResponse {
  token: string;
}

/** POST /userinfo response payload, merged into the access store. */
export interface UserInfo {
  userId: string;
  userName: string;
  nickName: string;
  userType: string;
  email: string;
  phonenumber: string;
  sex: string;
  avatar: string;
  permissions: string[];
  roles: string[];
}

/**
 * GET /webapp/getUIConfiguration response.
 * `disablePages` is read if present; the Go server currently omits it
 * (reconciliation #5) — treated as "no pages hidden" when absent.
 */
export interface UIConfiguration {
  authType: string;
  disablePages?: string[];
}

/** A backend cluster member (POST /webapp/getAllBackends). */
export interface BackendData {
  name: string;
  proxyTo: string;
  active: boolean;
  routingGroup: string;
  /** May be absent/blank — Go marks it omitempty (reconciliation #7). */
  externalUrl?: string;
  queued: number;
  running: number;
  status: string;
}

/** Payload for save/update/delete backend mutations. */
export interface ProxyBackend {
  name: string;
  routingGroup: string;
  proxyTo: string;
  externalUrl: string;
  active: boolean;
}

/** Dashboard distribution (POST /webapp/getDistribution). */
export interface DistributionDetail {
  totalBackendCount: number;
  offlineBackendCount: number;
  onlineBackendCount: number;
  healthyBackendCount: number;
  unhealthyBackendCount: number;
  totalQueryCount: number;
  averageQueryCountMinute: number;
  averageQueryCountSecond: number;
  distributionChart: DistributionChartData[];
  /** Keyed by backend name; Go currently returns {} (reconciliation #8). */
  lineChart: Record<string, LineChartData[]>;
  startTime: string;
}

export interface DistributionChartData {
  backendUrl: string;
  queryCount: number;
  name: string;
}

export interface LineChartData {
  epochMillis: number;
  backendUrl: string;
  queryCount: number;
  name: string;
}

/**
 * Query history request. Field names follow the Go contract
 * (reconciliation #1-3): userName / backendUrl / pageSize.
 */
export interface FindQueryHistoryRequest {
  userName: string;
  backendUrl: string;
  queryId: string;
  source: string;
  page: number;
  pageSize: number;
}

export interface QueryDetail {
  queryId: string;
  queryText: string;
  user: string;
  source: string;
  backendUrl: string;
  captureTime: number;
  routingGroup: string;
  /** Go currently leaves this blank (reconciliation #4); resolved client-side. */
  externalUrl: string;
}

/** Paginated table envelope (matches Go TableData[T]). */
export interface TableData<T> {
  total: number;
  rows: T[];
}

/** A routing rule (GET/POST /webapp/getRoutingRules). */
export interface RoutingRulesData {
  name: string;
  description: string;
  priority: number;
  actions: string[];
  condition: string;
}

/** Sentinel returned by the client when getRoutingRules answers HTTP 204. */
export interface ExternalRoutingMarker {
  isExternalRouting: true;
}
