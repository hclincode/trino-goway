---
title: Webapp Feature Inventory
author: ui-analyst
role: UI Analyst
component: trino-gateway
topics: [webapp, frontend, feature-parity]
date: 2026-06-03
status: draft
risk: high
related-to: [webapp-api-and-data-model.md, webapp-architecture-stack-and-ux.md]
---

# Webapp Feature Inventory

## Summary

The original trino-gateway webapp is a React 18 SPA with four pages: Dashboard, Cluster, History, and Routing Rules. It gates write operations on the ADMIN role and uses a `{code, msg, data}` JSON envelope from the backend. This document enumerates every page, UI element, interaction, and role gate so the rebuild can be built to 100% feature parity.

## Routes

| itemKey | Path | Component | Role gate |
|---|---|---|---|
| `dashboard` | `/dashboard` | `Dashboard` | none (all authenticated users) |
| `cluster` | `/cluster` | `Cluster` | none (all authenticated users) |
| `history` | `/history` | `History` | none (all authenticated users) |
| `routing-rules` | `/routing-rules` | `RoutingRules` | none (all authenticated users) |
| — | `/` | redirect → `/dashboard` | — |
| — | `*` (404) | `Home` (idle illustration + username greeting) | — |

Routes are filtered by two mechanisms (`src/router.tsx:86-111`):
1. `router.roles` — if non-empty, caller must have one of the listed roles.
2. `access.hasPermission(itemKey)` — server-returned permissions array; empty array means all pages allowed.
3. `disablePages` array from `GET /webapp/getUIConfiguration` — itemKeys listed there are hidden from the sidebar.

Currently all four routes have `roles: []`, so role-based page hiding is not yet used, but the infrastructure is in place.

---

## Page: Login (`/` — unauthenticated)

**Source:** `src/components/login.tsx`

### Purpose
Gate access to the app. Polls `/loginType` to decide which login variant to render.

### UI elements

| Element | Condition |
|---|---|
| Trino Gateway logo + "Sign in Trino Gateway Account" heading | always |
| Username + Password fields + "Sign in" button | `loginType === 'form'` |
| "Sign in with external authentication" button | `loginType === 'oauth'` |
| Username field (read-only) + Password field (read-only, placeholder "Password not allowed") + "Sign in" button | `loginType === 'none'` |
| Spinner (`Spin size="large"`) | `loginType === undefined` (loading) |

### Interactions
- **Form login:** validates both fields, calls `POST /login`, stores JWT in zustand access store; shows `Toast.success` on success.
- **OAuth login:** calls `POST /sso`, receives a redirect URL string, does `window.location.href = data` to navigate to the IdP.
- **No-auth login:** password field is `readonly`, only username matters; calls `POST /login` same as form.
- **Token from cookie:** on app mount, if `Cookies.get('token')` is set (e.g., after OIDC callback), it is consumed into the access store and the cookie is removed (`src/App.tsx:40-46`).

### States
- Loading: spinner while `loginType` is undefined.
- Error: network errors surface via the `ClientApi` global Toast.

---

## Page: Dashboard (`/dashboard`)

**Source:** `src/components/dashboard.tsx`

### Purpose
High-level gateway health and query throughput overview.

### UI elements

**Summary card** (Semi UI `<Descriptions>` with 9 key–value rows):

| Key | Value |
|---|---|
| Started at | `distributionDetail.startTime` formatted in the selected timezone |
| Backends | total backend count; clickable link to `/cluster` if caller has cluster page permission |
| Backends online | `onlineBackendCount` |
| Backends offline | `offlineBackendCount` |
| Backends healthy | `healthyBackendCount` |
| Backends unhealthy | `unhealthyBackendCount` |
| QPH | `totalQueryCount` (past-hour query count); tooltip: "The number of queries in the past hour" |
| Average QPM | `averageQueryCountMinute.toFixed(2)`; tooltip: "Average number of queries per minute in the past hour" |
| Average QPS | `averageQueryCountSecond.toFixed(2)`; tooltip: "Average number of queries per second in the past hour" |

**Line chart card** (2/3 width, ECharts line chart):
- Title: "Query distribution in last hour"
- X-axis: time labels (HH:MM) bucketed by minute, rendered in the selected timezone.
- Y-axis: query count (integer, minInterval 1).
- Series: one smooth line per backend name; legend shows backend names.
- Tooltip: axis-triggered (shows all series at a timestamp).
- Theme-aware legend color (observes `theme-mode` attribute on `<body>`).

**Distribution chart card** (1/3 width, ECharts doughnut/pie chart):
- Title: "Query distribution in last hour"
- Chart: donut, radius `['40%', '70%']`, rounded segments.
- Data: one slice per backend (`distributionChart[].name`, `distributionChart[].queryCount`).
- Emphasis label on hover (bold, fontSize 17).
- Tooltip: item-triggered.
- Theme-aware legend color.

**Timezone selector** (in top header, all pages):
- Semi UI `<Select>` with filter enabled.
- Options: all IANA timezone names from `@vvo/tzdb` (sorted alphabetically).
- Default: browser's `Intl.DateTimeFormat().resolvedOptions().timeZone` or `'UTC'`.
- Affects Dashboard timestamps; also used in History table timestamps.

### Interactions
- On mount, calls `POST /webapp/getDistribution` once (no auto-refresh).
- Clicking "Backends" count navigates to `/cluster` if user has permission.
- Timezone change re-renders line chart x-axis labels without re-fetching data.
- Theme switch (auto/light/dark) re-renders chart legend colors via `MutationObserver` on `<body>`.

### States
- Loading: no explicit loading state; values simply remain undefined (fields show as blank).
- Error: API errors surface as Toast via `ClientApi`.
- Empty: charts render empty axes/slices if no data returned.

---

## Page: Cluster (`/cluster`)

**Source:** `src/components/cluster.tsx`

### Purpose
List and manage Trino backend cluster members.

### UI elements

**Table** (Semi UI `<Table>`, no pagination, row key = `name`; sorted alphabetically by name on initial fetch):

| Column | Sortable | Filterable | Notes |
|---|---|---|---|
| Name | yes (alpha) | no | — |
| RoutingGroup | yes (alpha) | yes — dropdown of distinct values | multi-value filter |
| ProxyToUrl | no | no | rendered as external link (opens in new tab) |
| ExternalUrl | no | no | rendered as external link (opens in new tab) |
| Queued | yes (numeric) | no | integer |
| Running | yes (numeric) | no | integer |
| Active | no | no | `<Switch>` — ADMIN can toggle; non-ADMIN sees read-only switch |
| Status | no | no | `<Tag>` color: HEALTHY=green, UNHEALTHY=red, PENDING=yellow, UNKNOWN=white |
| Operations | ADMIN only | no | Edit + Delete (with confirmation popover) buttons |

**Create button**: shown as the column header of the Operations column (ADMIN only). Opens the Create modal.

**Create/Edit modal** (500×500, centered):
- Form fields: Name (required, disabled on edit), RoutingGroup (required), ProxyTo (required), ExternalUrl (required), Active (Switch, default false).
- Submit: Create calls `POST /webapp/saveBackend`; Edit calls `POST /webapp/updateBackend`.
- Success: table refreshes, Toast success.
- Error: Toast error.

**Delete confirmation popover** (Semi UI `Popconfirm`, position bottomRight):
- Confirm calls `POST /webapp/deleteBackend {name: record.name}`.
- Success: table refreshes, Toast success.
- Error: Toast error.

**Active switch** (`SwitchRender` sub-component):
- ADMIN only (non-ADMIN sees disabled switch).
- On toggle: calls `POST /webapp/updateBackend` with the full record but `active` flipped.
- Shows loading spinner during the request.

### Role gates
- Table column "Operations" and the Create button: ADMIN only (`access.hasRole(Role.ADMIN)`).
- Active switch: disabled for non-ADMIN.

### States
- Loading: no explicit loading state; table is empty until `backendsApi` resolves.
- Error: Toast on any API failure.
- Empty: empty table when no backends exist.

---

## Page: History (`/history`)

**Source:** `src/components/history.tsx`

### Purpose
Browse paginated Trino query history with filters.

### UI elements

**Filter bar card** (horizontal form):

| Field | Type | Notes |
|---|---|---|
| RoutedTo | Select (clearable) | Options: all backends by externalUrl; each option shows name tag + URL. Default: all. |
| User | Text input (clearable) | Pre-filled from `access.userName`; read-only for non-ADMIN. Persisted to `sessionStorage` key `'username'`. |
| QueryId | Text input (clearable) | placeholder "Default all" |
| Source | Text input (clearable) | — |
| Query button | Submit | Triggers list(1) |

**Results table card** (paginated, pageSize=15):

| Column | Notes |
|---|---|
| QueryId | External link to `{record.externalUrl}/ui/query.html?{queryId}` in new tab |
| RoutingGroup | Text; column has sortable + filterable by routing group (same filter logic as Cluster) |
| Name | Resolved via `backendMapping[backendUrl]` (maps internal URL → backend name) |
| RoutedTo | External link (externalUrl) in new tab |
| User | Plain text |
| Source | Plain text |
| QueryText | Truncated with ellipsis tooltip, 300px width; clickable to open SQL modal |
| SubmissionTime | `captureTime` (epoch ms) formatted in selected timezone; sortable |

**SQL query text modal** (large modal, max-height 60vh, scrollable):
- Shows full query text via `<CodeHighlight language="sql" lineNumber>` with word-wrap.
- "Copy to Clipboard" button (ok button); on success changes label to "Copied!", on failure to "Copy Failed".

### Role gates
- User filter field: disabled (read-only) for non-ADMIN. Non-ADMIN users can only view their own queries (enforced server-side in `webappFindQueryHistory`).

### States
- Filter form submits on each change (re-fetches from page 1).
- Pagination: server-side, driven by `total` from API.
- `sessionStorage` preserves the `user` filter across page navigations (not across browser sessions).

---

## Page: Routing Rules (`/routing-rules`)

**Source:** `src/components/routing-rules.tsx`

### Purpose
View (and for ADMIN, edit) the gateway's routing rule list.

### UI elements

**Loading state**: centered `<Spin size="large">` while fetching.

**External routing notice** (when server returns HTTP 204 or `{isExternalRouting: true}`):
- Text: "No routing rules available. Routing rules are managed by an external service."

**Empty notice** (when `rules.length === 0` and not external):
- Text: "No routing rules configured. Add rules to manage query routing."

**Rule cards** (one Semi UI `<Card>` per rule, `shadows='always'`, max-width 800):
- Card title: `Routing rule #N` (1-indexed).
- Header "Edit" button: ADMIN only.
- Footer "Save" button: ADMIN only, visible only when card is in edit mode.

Each card contains a form with:

| Field | Type | Editable | Notes |
|---|---|---|---|
| Name | Text input | never (always disabled) | identity of the rule |
| Description | Text input | yes (when editing) | optional |
| Priority | Text input | yes (when editing) | numeric displayed as string |
| Condition | Text input | yes (when editing) | required |
| Actions | TextArea (autosize) | yes (when editing) | comma-separated list; split/trimmed on save |

- Each rule card has independent edit state (only the clicked rule enters edit mode).
- Save calls `POST /webapp/updateRoutingRules` with the full rule object.
- Success: card exits edit mode, in-memory rule updated, Toast success.
- Error: Toast error.

### Role gates
- Edit and Save buttons: ADMIN only.
- Read: all authenticated users.

---

## Global Shell (`RootLayout`)

**Source:** `src/components/layout.tsx`

### Header bar (fixed, 60px height)

| Element | Notes |
|---|---|
| Logo (SVG) + "Trino Gateway" text | left side |
| Timezone selector | right side, always visible |
| Theme toggle button | cycles auto → light → dark; icon: sun/moon/mark |
| User dropdown button | ADMIN shows orange gear icon; non-ADMIN shows blue user icon |

**User dropdown items:**
- "Profile" — opens the user profile modal.
- "Logout" — calls `POST /logout`, clears token, Toast success.

**User profile modal** (400×400, no cancel button, no close button visible):
- Avatar (from `access.avatar` or config default logo).
- Username (large bold text).
- User ID (with ID card icon).
- Role tags: ADMIN = orange, other roles = blue.

### Sidebar (fixed, collapsible)
- Default width: 240px. Collapsed: 60px.
- Collapse/expand toggle at sidebar bottom.
- Navigation items filtered by `disablePages` (from `getUIConfiguration`) and role permissions.
- Active page highlighted; synchronized with react-router location.
- Content area has `margin-left` transition (0.3s ease) matching sidebar state.

---

## Parity Checklist

This is the flat acceptance-criteria list for the rebuild. Every item must be verified.

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
- [ ] Dark mode: applies `theme-mode="dark"` on `<body>` for Semi UI; meta theme-color updated
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
- [ ] Table: columns Name, RoutingGroup, ProxyToUrl, ExternalUrl, Queued, Running, Active, Status, Operations
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
- [ ] All user-facing strings sourced from locale file (no hardcoded English strings outside locale)
- [ ] Semi UI component locale set via `<LocaleProvider>`

### Error handling
- [ ] Network errors show toast "The network has wandered off, try again later"
- [ ] 401/403 HTTP responses clear session and show expiry toast
- [ ] 500-level API responses show the server `msg` as toast
- [ ] React `ErrorBoundary` wraps entire app; shows error + stack on render crash
