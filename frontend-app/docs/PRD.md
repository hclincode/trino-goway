# Product Requirements Document — trino-goway Web UI Rebuild

**Date:** 2026-06-03
**Status:** Draft
**Owner:** frontend-architect
**Stack:** locked in `frontend-app/docs/CONVENTIONS.md`
**Basis:** `docs/studies/webapp-feature-inventory.md`, `docs/studies/webapp-api-and-data-model.md`, `docs/studies/webapp-architecture-stack-and-ux.md`

---

## What Is This

A from-scratch rebuild of the trino-gateway admin web UI as a modern React 19
single-page app, replacing the original Semi UI / React 18 SPA. The new app is
built to a static bundle and embedded/served by the Go gateway under the
`/trino-gateway/` base path (replacing `cmd/trino-goway/web/dist`).

The original ships four authenticated pages plus a login screen, gated by an
`ADMIN` role and an optional page-permission list, talking to the gateway through a
`{code, msg, data}` JSON envelope. This rebuild reproduces all of that behavior
with the locked modern stack (Vite + React 19 + TS strict + antd v5 + TanStack
Query v5 + Zustand v5 + ECharts).

---

## Goals

- **100% feature parity** with the original UI across login, shell/layout,
  dashboard, cluster, history, and routing-rules — measured by the 60-item parity
  checklist (see Acceptance Criteria).
- Modern, maintainable frontend: typed end-to-end against the Go contract, server
  state via TanStack Query, accessible antd components, theme-aware charts.
- A production static bundle that drops into the Go gateway under
  `/trino-gateway/` with minimal Go-side change.
- A typed API client that is the single place envelope/token/401 handling lives.

---

## Scope (Build This)

**Pages**
- **Login** (`/` unauthenticated): form / oauth / no-auth variants driven by
  `loginType`; OIDC cookie hand-off; logout.
- **Dashboard** (`/dashboard`): summary card (9 rows), ECharts line chart,
  ECharts doughnut chart, timezone-aware timestamps.
- **Cluster** (`/cluster`): sortable/filterable backend table, active toggle,
  create/edit modal, delete confirmation, ADMIN role gating.
- **History** (`/history`): paginated query table, four filters, SQL modal with
  copy-to-clipboard, deep links, timezone-aware submission time.
- **Routing Rules** (`/routing-rules`): card-per-rule with independent inline edit,
  external-routing notice (204 handling), empty state.

**Shell**
- Fixed header (logo, app name, timezone selector, theme toggle, user dropdown).
- Collapsible sidebar (240px ↔ 60px) with role/permission/`disablePages` filtering
  and route-synced active item.
- User profile modal; theme cycle auto → light → dark (persisted).
- App-wide React error boundary.

**Cross-cutting**
- Typed API client (envelope, JWT bearer, 204→external-routing, 401/403→expiry).
- TanStack Query data layer; Zustand persisted stores for token+user and config.
- Timezone context propagated to dashboard + history.
- react-i18next, en_US locale, no hardcoded UI strings, antd locale wired.
- Toast/notification on success/error per the original.

---

## Non-Goals

- **No new features** beyond parity. The dead `global-property` endpoints
  (`findGlobalProperty`/`getGlobalProperty`/`save`/`update`/`delete`) are **omitted**
  — they have no callers and no Go handlers.
- **No backend/Go changes from this team.** Where the Go contract is wrong or
  incomplete, the frontend aligns to the *actual* Go behavior and degrades
  gracefully; required Go fixes are logged below as backend dependencies for a
  separate hand-off.
- No multi-language localization at launch (en_US only; i18n stays pluggable).
- No persisted timezone (matches original — in-memory, resets on refresh) unless a
  parity item says otherwise. (Original does not persist it.)
- No Playwright e2e at launch (optional, later).
- No fixing of `Queued`/`Running` always-0 or empty `lineChart` from the UI — these
  are Go-side data gaps; the UI renders what it receives.

---

## API Reconciliation

The original frontend and the Go `internal/admin` server disagree on several field
names, methods, and payloads (see `webapp-api-and-data-model.md` §"API Mismatch
Summary"). **Policy: the new frontend ALIGNS to the Go gateway's ACTUAL contract**
wherever the frontend can fix it on its side. Where the gap is a Go data/shape
limitation, the UI degrades gracefully AND we log the Go fix as a backend
dependency. Cross-reference: `docs/topics/gateway-docs-compatibility-audit.md`.

| # | Endpoint | Mismatch | Frontend policy (this rebuild) | Go-side dependency (noted, not done here) |
|---|---|---|---|---|
| 1 | `findQueryHistory` | FE sends `user`, Go reads `userName` | **Send `userName`** | none — FE fix is sufficient |
| 2 | `findQueryHistory` | FE sends `externalUrl`, Go reads `backendUrl` | **Send `backendUrl`** (the backend's URL, from the selected option) | none — FE fix is sufficient |
| 3 | `findQueryHistory` | FE sends `size`, Go reads `pageSize` | **Send `pageSize`** (15) and `page` | none — FE fix is sufficient |
| 4 | `findQueryHistory` resp | Go never populates `externalUrl` (always `""`) | **Resolve `externalUrl` client-side from the backend list** (map `backendUrl`→backend→`externalUrl`); if unresolved, render QueryId/RoutedTo as plain text (no broken link) | *Backend dep:* populate `ExternalURL` in `queryDetailFromRecord` so the link is authoritative |
| 5 | `getUIConfiguration` resp | FE reads `disablePages`, Go struct has only `authType` | **Treat missing `disablePages` as "no pages hidden"** (all visible). Read it if present | *Backend dep:* add `DisablePages []string` to `UIConfiguration` for page hiding to work |
| 6 | `getRoutingRules` | FE uses GET, Go registers POST → 405 | **Call it as the Go router registers it** (POST). Confirm the verb against `internal/admin/router.go` at integration; keep it a one-line switch | *Backend dep (optional):* if Go intends GET, change the route; FE follows whatever the router actually serves |
| 7 | `getAllBackends` resp | `externalUrl` is `omitempty` (absent when blank) | **Treat absent/blank `externalUrl` as "no external link"**: render the cell/links as plain text, never a dead `href` | *Backend dep (optional):* always emit `externalUrl` |
| 8 | `getDistribution` resp | `lineChart` always `{}` (empty) | **Render the line chart with empty series + an "no data" affordance**; doughnut/summary still work | *Backend dep:* populate `lineChart` from query history for the chart to show data |

Operating rule for #6: the frontend client method is written to match the verb the
Go router actually serves at integration time; verify against
`internal/admin/router.go` rather than the study, since the study flags this as a
live discrepancy.

---

## Backend Dependencies (hand-off list)

These are **not** done by the frontend team. They are the Go-side changes required
for full data fidelity; until they land, the UI degrades gracefully as described
above. Report these to the team-lead for routing into the Go `TODO`, and
cross-reference `docs/topics/gateway-docs-compatibility-audit.md`.

1. **`findQueryHistory` response `externalUrl`** — populate from backend mapping so
   QueryId/RoutedTo links are authoritative rather than client-reconstructed.
2. **`getUIConfiguration.disablePages`** — add the field if server-driven page
   hiding is ever wanted; until then the sidebar shows all permitted pages.
3. **`getDistribution.lineChart`** — populate per-backend minute buckets so the
   dashboard line chart shows real data.
4. **`getRoutingRules` verb / payload** — confirm GET vs POST and the
   `updateRoutingRules` behavior (currently a stub returning `[]`).
5. **`getAllBackends.externalUrl`** — emit consistently (drop `omitempty`) if
   external links should always render.

---

## Acceptance Criteria — 60-item Parity Checklist

These are the exact acceptance criteria, reproduced from
`docs/studies/webapp-feature-inventory.md` §"Parity Checklist". Each item must be
verified (component test and/or manual) before the corresponding page is "done".
The study remains authoritative; this list is the contract.

### Auth / Login
- [ ] Detect login type via `POST /loginType`; show spinner while loading
- [ ] Form login: username + password fields, validation, `POST /login`, store JWT
- [ ] OAuth login: single button, `POST /sso`, redirect to returned URL
- [ ] No-auth login: username only (password read-only), `POST /login`
- [ ] Consume `token` cookie from OIDC callback on app mount, then remove cookie
- [ ] Logout: `POST /logout`, clear token, show success toast
- [ ] Session expiry (401/403): auto-clear token, show expiry toast

### Shell / Layout
- [ ] Fixed header: logo, app name, timezone selector, theme toggle, user dropdown
- [ ] Theme toggle: auto / light / dark cycle; persisted in zustand config store
- [ ] Dark mode applied app-wide (antd `theme.darkAlgorithm`; meta theme-color updated)
- [ ] User dropdown: Profile modal + Logout
- [ ] User profile modal: avatar, username, user ID, roles (ADMIN=orange, other=blue)
- [ ] Sidebar: collapsible (240px ↔ 60px) with smooth transition
- [ ] Sidebar items filtered by `disablePages` from `getUIConfiguration`
- [ ] Sidebar items filtered by role permissions
- [ ] Active item highlighted; synced to route
- [ ] Content margin adjusts with sidebar collapse/expand

### Dashboard
- [ ] Summary card with 9 key-value rows (start time, backend counts, QPH/QPM/QPS)
- [ ] QPH/QPM/QPS have help tooltip
- [ ] "Backends" value is a clickable link to `/cluster` (only if user has cluster permission)
- [ ] Timezone selector in header; affects all time displays
- [ ] Line chart: one series per backend, x-axis = minute buckets in selected timezone, smooth lines
- [ ] Line chart: theme-aware legend color (reacts to dark/light mode switch)
- [ ] Doughnut chart: one slice per backend, hover emphasis label, theme-aware legend
- [ ] Both charts: data from `POST /webapp/getDistribution`

### Cluster
- [ ] Table columns: Name, RoutingGroup, ProxyToUrl, ExternalUrl, Queued, Running, Active, Status, Operations
- [ ] Name, RoutingGroup, Queued, Running columns sortable
- [ ] RoutingGroup column filterable by distinct values
- [ ] ProxyToUrl and ExternalUrl rendered as external links
- [ ] Status column: colored tag (HEALTHY=green, UNHEALTHY=red, PENDING=yellow, UNKNOWN=white)
- [ ] Active column: toggle switch; ADMIN can toggle; non-ADMIN sees disabled switch
- [ ] Active toggle: calls updateBackend immediately, shows loading state
- [ ] Operations column (ADMIN only): Edit + Delete buttons
- [ ] Create button in Operations column header (ADMIN only)
- [ ] Create/Edit modal: Name (disabled on edit), RoutingGroup, ProxyTo, ExternalUrl, Active fields
- [ ] Delete: confirmation popover before calling deleteBackend
- [ ] Success/error toasts for all mutations
- [ ] Table sorted alphabetically by name on load

### History
- [ ] Filter bar: RoutedTo (select with backend options), User (prefilled, ADMIN-only editable), QueryId, Source, Query button
- [ ] User field persisted to sessionStorage across navigations
- [ ] Non-ADMIN: user field read-only (enforced client-side; also server-side)
- [ ] Table: QueryId, RoutingGroup, Name, RoutedTo, User, Source, QueryText, SubmissionTime columns
- [ ] QueryId: link to `{externalUrl}/ui/query.html?{queryId}` in new tab
- [ ] RoutingGroup: sortable + filterable
- [ ] Name: resolved from backend URL via mapping (not raw URL)
- [ ] RoutedTo: external link in new tab
- [ ] QueryText: truncated with tooltip; clickable to open full-text SQL modal
- [ ] SQL modal: syntax-highlighted, line numbers, word-wrap, copy-to-clipboard button
- [ ] SubmissionTime: formatted in selected timezone, sortable
- [ ] Server-side pagination (pageSize=15); re-fetches on page change
- [ ] Filters trigger re-fetch from page 1

### Routing Rules
- [ ] Loading spinner while fetching
- [ ] External routing notice when server returns 204 / `isExternalRouting: true`
- [ ] Empty state notice when no rules configured
- [ ] One card per rule: title `Routing rule #N`, fields: Name (read-only), Description, Priority, Condition, Actions
- [ ] Actions displayed as comma-joined string; saved as array
- [ ] Cards have independent edit mode; only ADMIN sees Edit/Save buttons
- [ ] Save calls `POST /webapp/updateRoutingRules`
- [ ] Success/error toasts

### Timezone
- [ ] Timezone dropdown in header (all pages)
- [ ] Options from `@vvo/tzdb` sorted alphabetically, with UTC
- [ ] Default: browser timezone or UTC
- [ ] Context propagates to Dashboard (line chart x-axis) and History (SubmissionTime column)

### i18n / Locale
- [ ] Default locale: en_US
- [ ] All user-facing strings sourced from locale file (no hardcoded English outside locale)
- [ ] antd component locale set via `ConfigProvider locale`

### Error handling
- [ ] Network errors show toast "The network has wandered off, try again later"
- [ ] 401/403 HTTP responses clear session and show expiry toast
- [ ] 500-level / non-200 envelope responses show the server `msg` as toast
- [ ] React `ErrorBoundary` wraps entire app; shows error + stack on render crash

---

## Success Criteria

1. All 60 parity items verified (component tests where feasible, otherwise a manual
   verification note).
2. DoD gate green: `pnpm typecheck && pnpm lint && pnpm test && pnpm build`.
3. `vite build` produces a static `dist/` that loads under `/trino-gateway/` and
   exercises all five pages against a running Go gateway (or recorded fixtures).
4. No hardcoded UI strings outside the locale file; types match the Go JSON
   contract.
5. Graceful degradation verified for the four Go-side data gaps (#4, #5, #7, #8) —
   no broken links, no crashes, sensible empty states.

---

## Milestones

| Milestone | Content | DoD |
|---|---|---|
| **M0 — Scaffold** | pnpm + Vite + React 19 + TS strict, ESLint/Prettier, Vitest, scripts, base config, `.gitignore`, committed lockfile | gates green on an empty app |
| **M1 — Core infra** | API client, TanStack Query, browser router + base path + guards, app shell/layout, theme, i18n bootstrap, auth/login + token + OIDC cookie + logout | gates green; login + empty shell render and route |
| **M2 — Dashboard** | summary card + line + doughnut charts + timezone | gates green; dashboard parity items checked |
| **M3 — Cluster** | table + sort/filter + active toggle + CRUD modal + delete confirm + role gating | gates green; cluster parity items checked |
| **M4 — History** | paginated table + 4 filters + SQL modal + deep links + timezone | gates green; history parity items checked |
| **M5 — Routing Rules** | card-per-rule + inline edit + 204/external + empty states | gates green; routing-rules parity items checked |
| **M6 — Polish & wiring** | accessibility pass, full test sweep, production build, Go-embed wiring notes | all 60 items verified; DoD green; bundle loads under base path |

See `frontend-app/docs/TODO.md` for the phased, checkbox task breakdown.

---

*Reference: `frontend-app/docs/CONVENTIONS.md` · `frontend-app/docs/studies/*` · `docs/topics/gateway-docs-compatibility-audit.md`*
