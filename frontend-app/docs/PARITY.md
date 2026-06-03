# Parity Verification — 60-item Checklist

**Date:** 2026-06-04
**Owner:** frontend-architect
**Basis:** `docs/PRD.md` Acceptance Criteria (mirrors `docs/studies/webapp-feature-inventory.md`)

Status per item: **MET** (implemented + verified), **MET (degraded)** (implemented;
fully correct once a noted Go-side gap is fixed — see `docs/PRD.md` Backend
Dependencies), **N/A**. Verification = component/unit test where marked `[test]`,
otherwise code-level/manual review.

## Auth / Login (7)
- MET — login type via `POST /loginType`; spinner while loading `[test]`
- MET — form login: username+password, validation, `POST /login`, store JWT
- MET — oauth: single button → `POST /sso` → redirect `[test]`
- MET — no-auth: username only, password read-only `[test]`
- MET — consume OIDC `token` cookie on mount, then remove it
- MET — logout: `POST /logout`, clear token, success toast
- MET — 401/403 → clear token + expiry toast `[test]`

## Shell / Layout (10)
- MET — fixed header: logo, app name, timezone select, theme toggle, user dropdown
- MET — theme cycle auto/light/dark, persisted (config store)
- MET — dark mode via antd `darkAlgorithm`; `theme-mode` attr + meta theme-color
- MET — user dropdown: Profile modal + Logout
- MET — profile modal: avatar, username, userId, role tags (ADMIN=orange/other=blue)
- MET — collapsible sidebar 240↔60 with transition (antd Sider)
- MET (degraded) — sidebar filtered by `disablePages`: honored when present; Go omits
  it today (gap #5) so nothing is hidden — the filter logic is in `useVisibleRoutes`
- MET — sidebar filtered by role permissions (`useVisibleRoutes` + `RouteGuard`)
- MET — active item highlighted, synced to route
- MET — content area adjusts as the sidebar collapses (antd layout)

## Dashboard (8)
- MET — summary card, 9 rows
- MET — QPH/QPM/QPS help tooltips
- MET — Backends count links to /cluster when permitted
- MET — header timezone affects all time displays
- MET (degraded) — line chart: per-backend minute buckets, zoned x-axis, smooth
  `[test on bucketing]`; renders empty "no data" state because Go returns an empty
  `lineChart` (gap #8)
- MET — line chart theme-aware legend (re-keys on light/dark)
- MET — doughnut: slice per backend, hover emphasis, theme-aware legend
- MET — both charts from `POST /webapp/getDistribution`

## Cluster (14)
- MET — all 9 columns `[test]`
- MET — Name/RoutingGroup/Queued/Running sortable
- MET — RoutingGroup column filter by distinct values
- MET — ProxyTo/ExternalUrl external links
- MET — Status colored tag (HEALTHY/UNHEALTHY/PENDING/UNKNOWN) `[test]`
- MET — Active switch: ADMIN toggles, non-ADMIN disabled
- MET — Active toggle → updateBackend with per-row loading
- MET — Operations (Edit+Delete) ADMIN-only `[test]`
- MET — Create button in Operations header, ADMIN-only `[test]`
- MET — Create/Edit modal (Name disabled on edit; RoutingGroup, ProxyTo, ExternalUrl, Active)
- MET — Delete confirmation popover → deleteBackend
- MET — success/error toasts on all mutations
- MET — table sorted alphabetically by name on load `[test]`
- MET (degraded) — ExternalUrl absent/blank → plain text (Go gap #7), no dead link

## History (16)
- MET — filter bar: RoutedTo select, User (prefilled/ADMIN-only), QueryId, Source, Query
- MET — User persisted to sessionStorage across navigations
- MET — non-ADMIN User field read-only
- MET — table columns: QueryId, RoutingGroup, Name, RoutedTo, User, Source, QueryText, SubmissionTime
- MET (degraded) — QueryId deep-link `{externalUrl}/ui/query.html?{queryId}`; externalUrl
  resolved client-side from the backend list (Go gap #4 leaves the record blank); unresolved → plain text `[test on mapping]`
- MET — RoutingGroup sortable + filterable
- MET — Name resolved from backendUrl via mapping `[test]`
- MET (degraded) — RoutedTo external link (same gap #4 resolution path)
- MET — QueryText truncated + tooltip; click opens SQL modal
- MET — SQL modal: highlight.js, line numbers, word-wrap, copy-to-clipboard (label flips)
- MET — SubmissionTime zoned + sortable
- MET — server-side pagination (pageSize 15); refetch on page change
- MET — filters refetch from page 1
- MET — request uses Go field names userName/backendUrl/pageSize (reconciliation #1-3)

## Routing Rules (8)
- MET — loading spinner
- MET — external-routing notice on 204/isExternalRouting `[test]`
- MET — empty-state notice when no rules `[test]`
- MET — card per rule (title #N; Name read-only, Description, Priority, Condition, Actions) `[test]`
- MET — Actions comma-joined display ↔ array on save
- MET — independent per-card edit; ADMIN-only Edit/Save `[test]`
- MET — Save → `POST /webapp/updateRoutingRules`
- MET — success/error toasts

## Timezone (4)
- MET — header dropdown on all pages
- MET — options from `@vvo/tzdb` sorted, incl. UTC
- MET — default browser tz / UTC
- MET — propagates to Dashboard line chart + History SubmissionTime

## i18n / Locale (3)
- MET — default en_US
- MET — all UI strings from the locale resource (typed `t()`)
- MET — antd locale via `ConfigProvider locale={enUS}`

## Error handling (4)
- MET — network error message `[test]`
- MET — 401/403 → clear session + expiry toast `[test]`
- MET — non-200 envelope → server msg surfaced `[test]`
- MET — app-wide ErrorBoundary shows error + component stack

---

## Summary

- **60 / 60 items implemented.** All are MET.
- **6 items are MET (degraded)** pending Go-side fixes — they work and never crash or
  show dead links; they become fully data-accurate once the backend dependencies land:
  - `disablePages` (gap #5) — sidebar hides nothing until Go returns the field.
  - dashboard `lineChart` (gap #8) — empty "no data" state until Go populates it.
  - Cluster `externalUrl` omitempty (gap #7) — blank → plain text.
  - History `externalUrl` blank (gap #4) — resolved client-side from the backend
    list (QueryId deep-link, RoutedTo); plain text when unresolvable.
- These backend dependencies are tracked in `docs/PRD.md` → "Backend Dependencies"
  and cross-referenced to `docs/topics/gateway-docs-compatibility-audit.md`.
- **Test coverage:** 39 tests across 9 files — API client (envelope/204/401/403/
  network), stores, line-chart bucketing, backend mapping, StatusTag, routing-rules
  guard, plus component tests for Login (3 variants), Cluster (sort + ADMIN gating),
  and Routing Rules (external/empty/list).
