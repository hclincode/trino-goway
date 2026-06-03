# Frontend Rebuild — Phased TODO

**Date:** 2026-06-03
**Owner:** frontend-architect
**Governs:** `frontend-app/docs/CONVENTIONS.md` (stack + DoD) · `frontend-app/docs/PRD.md` (scope + parity)

Each task is small and independently verifiable. **Every task ends with the DoD
gate** — it is not done until all four pass:

```
pnpm typecheck && pnpm lint && pnpm test && pnpm build
```

Claim a task (set owner) and mark it `in_progress` before starting; mark
`completed` only when its gate is green. Parity items in **(parity: …)** notes map
to the checklist in `PRD.md`.

---

## Phase 0 — Scaffold

- [x] **0.1** Init pnpm project; create Vite + React 19 + TS app under `frontend-app/`.
  Set Vite `base: '/trino-gateway/'`. **Gate.**
- [x] **0.2** TypeScript strict config (`strict: true`, `noUncheckedIndexedAccess`,
  path alias `@/` → `src/`). **Gate.**
- [x] **0.3** ESLint flat config (`typescript-eslint`, `react-hooks`, `jsx-a11y`) +
  Prettier; add `lint`, `lint:fix`, `format` scripts. **Gate.**
- [x] **0.4** Vitest + React Testing Library + jsdom; add `test`/`test:watch`; one
  trivial passing smoke test. **Gate.**
- [x] **0.5** Define all DoD scripts in `package.json` exactly as in CONVENTIONS
  (`dev`, `build`, `preview`, `typecheck`, `lint`, `test`). **Gate.**
- [x] **0.6** `.gitignore` (node_modules, dist, local env); **commit
  `pnpm-lock.yaml`**; copy `public/logo.svg` + favicon from original assets. **Gate.**

> **Phase 0 toolchain notes (resolved during scaffold):**
> - **vitest pinned to v3** (not v2): vitest v2 bundles `vite@5` and its
>   `vitest/config` `defineConfig` is typed against Vite 5's `Plugin`, which
>   conflicts with the installed `vite@6` plugin types under `tsc -b`. v3 is built
>   against Vite 6, deduping the tree to a single `vite@6.4.3`.
> - `vite.config.ts` imports `defineConfig` from **`vitest/config`** (not `vite`)
>   so the `test` block type-checks.
> - pnpm 11 records the esbuild build-script approval in **`pnpm-workspace.yaml`**
>   (`allowBuilds: { esbuild: true }`) — commit it alongside `pnpm-lock.yaml`.

## Phase 1 — Core infrastructure

- [x] **1.1** Zustand stores: `access` (token, userId, userName, roles[],
  permissions[], avatar, …; persist to localStorage) with `hasRole`/`hasPermission`;
  `config` (theme auto/light/dark; persist). **Gate.**
- [x] **1.2** Typed API client (`src/api/client.ts`): `{code,msg,data}` unwrap,
  `Authorization: Bearer` + `Content-Language: en_US`, `204→{isExternalRouting:true}`,
  401/403→clear token + expiry signal, network-error message. Unit-test envelope +
  204 + 401 paths. **(parity: error handling, session expiry)** **Gate.**
- [x] **1.3** Endpoint modules in `src/api/endpoints/` typed to the Go contract,
  applying the PRD API-reconciliation field fixes (`userName`/`backendUrl`/`pageSize`;
  routingRules verb per `internal/admin/router.go`). Shared `src/types/`. **Gate.**
- [x] **1.4** TanStack Query: `QueryClient` + provider; conventions for query keys and
  mutation invalidation. **Gate.**
- [x] **1.5** Providers wrapper: `QueryClientProvider` + antd `ConfigProvider`
  (theme algorithm + locale) + `I18nextProvider` + app `ErrorBoundary`
  (shows error + stack). **(parity: ErrorBoundary, antd locale)** **Gate.**
- [x] **1.6** i18n bootstrap (react-i18next), `locales/en_US.ts` (typed), detection
  order (localStorage `lang` → navigator → en_US). **(parity: i18n)** **Gate.**
- [x] **1.7** Router: `createBrowserRouter` with `basename="/trino-gateway"`, route
  table (`/`→`/dashboard`, four pages, `*`→idle/404), and guards for `roles` +
  `hasPermission` + `disablePages`. **(parity: routes, sidebar filtering)** **Gate.**
- [x] **1.8** App shell `RootLayout`: fixed header (logo, app name, slots for
  timezone/theme/user), collapsible sidebar (240↔60, transition, route-synced active
  item, content margin), responsive. **(parity: shell/layout items)** **Gate.**
- [x] **1.9** Theme toggle: auto→light→dark cycle, persisted; apply antd
  `darkAlgorithm`, update `<meta theme-color>`; expose `useTheme`. **(parity: theme,
  dark mode)** **Gate.**
- [x] **1.10** User dropdown + ProfileModal (avatar, username, userId, role tags
  ADMIN=orange/other=blue) + Logout (`POST /logout`, clear token, toast). **(parity:
  user dropdown, profile modal, logout)** **Gate.**
- [x] **1.11** TimezoneContext: default browser tz/UTC, `@vvo/tzdb` sorted options,
  header `Select`; expose `useTimezone` + zoned-format utils (Intl-based). **(parity:
  timezone)** **Gate.**
- [x] **1.12** Auth/Login page: poll `loginType` (spinner while loading); render
  form / oauth / no-auth variants; `POST /login` (store JWT, fetch userinfo),
  `POST /sso` (redirect), no-auth (readonly password). Consume `token` cookie on
  mount then remove it (OIDC hand-off). **(parity: all 7 auth items)** **Gate.**

> **Phase 1 notes:**
> - Node 26's experimental `localStorage` global throws unless `--localstorage-file`
>   is set, which broke zustand's default persist storage under Vitest. Added a
>   guarded `src/stores/storage.ts` (localStorage → in-memory fallback) used by both
>   persisted stores.
> - `date-fns-tz` ended up unused — zoned formatting uses the native `Intl`
>   `toLocaleString`/`DateTimeFormat` approach (matches the original webapp exactly).
>   Kept the dep for Phase 2 if richer formatting is needed; otherwise drop it in Phase 6.
> - Two non-blocking `react-refresh/only-export-components` lint *warnings*
>   (router.tsx, timezone.tsx) — HMR hints, 0 errors. Quieted in Phase 6 polish.
> - Main JS chunk ~1.16 MB (antd + echarts + router); per-page routes are already
>   lazy/code-split. `manualChunks` vendor splitting deferred to Phase 6.

## Phase 2 — Dashboard (`/dashboard`)

- [x] **2.1** `dashboard` query hook → `POST /webapp/getDistribution`; types. **Gate.**
- [x] **2.2** Summary card: antd `Descriptions`, 9 rows (start time zoned, backend
  counts, QPH/QPM/QPS with `.toFixed(2)`), help `Tooltip` on QPH/QPM/QPS, "Backends"
  link to `/cluster` when permitted. **(parity: summary card, tooltips, link)** **Gate.**
- [x] **2.3** ECharts line chart via `echarts-for-react`: one smooth series per
  backend, x-axis minute buckets in selected tz, `yAxis.minInterval:1`, axis tooltip;
  graceful empty state when `lineChart` is `{}` (Go gap #8). **(parity: line chart)**
  **Gate.**
- [x] **2.4** ECharts doughnut chart: slice per backend, `radius:['40%','70%']`,
  hover emphasis label, item tooltip. **(parity: doughnut)** **Gate.**
- [x] **2.5** Theme-aware legend colors for both charts: re-key charts on the
  resolved light/dark mode (`useChartColors`), replacing the original
  body-attribute MutationObserver. **(parity: theme-aware legends)** **Gate.**

> **Phase 2 notes:**
> - Line-chart minute-bucketing extracted to a pure `lineChart.ts` and unit-tested
>   (empty input → empty model; cross-backend alignment; same-minute summing).
> - Charts re-render on theme switch via a `key={mode}` on `ReactECharts` driven by
>   `useChartColors` — simpler and more reliable than observing a body attribute.
> - DashboardPage is lazy-loaded, so ECharts (~1.1 MB) sits in its own route chunk
>   rather than the entry bundle.

## Phase 3 — Cluster (`/cluster`)

- [x] **3.1** `backends` query → `POST /webapp/getAllBackends`; sort alpha by name on
  load; mutations (`saveBackend`/`updateBackend`/`deleteBackend`) invalidate
  `['backends']`. **(parity: load sort)** **Gate.**
- [x] **3.2** antd `Table` columns: Name, RoutingGroup, ProxyToUrl, ExternalUrl,
  Queued, Running, Active, Status, Operations. Sortable Name/RoutingGroup/Queued/
  Running; RoutingGroup column filter by distinct values. **(parity: columns,
  sort/filter)** **Gate.**
- [x] **3.3** External-link cells (ProxyToUrl/ExternalUrl) in new tab; handle
  absent/blank `externalUrl` as plain text (Go gap #7). Status `Tag` colors
  (HEALTHY=green/UNHEALTHY=red/PENDING=gold/UNKNOWN=default). **(parity: links,
  status tag)** **Gate.**
- [x] **3.4** Active `Switch` column: ADMIN toggles → `updateBackend` (full record,
  flipped `active`) with per-row loading; non-ADMIN disabled. **(parity: active
  toggle)** **Gate.**
- [x] **3.5** Operations column + Create button in its header, ADMIN-only; Create/Edit
  `Modal` (react-hook-form + zod): Name (disabled on edit), RoutingGroup, ProxyTo,
  ExternalUrl, Active. Create→`saveBackend`, Edit→`updateBackend`. **(parity:
  operations, create button, modal)** **Gate.**
- [x] **3.6** Delete `Popconfirm` → `deleteBackend {name}`; success/error toasts on
  all mutations; table refreshes via invalidation. **(parity: delete confirm,
  toasts)** **Gate.**

> **Phase 3 notes:**
> - Reusable `StatusTag` + `ExternalLink` live in `src/components/` (History reuses
>   both in Phase 4). `ExternalLink` degrades a blank/absent URL to plain text
>   (Go gap #7).
> - Active toggle, modal submit, and delete each surface success/error toasts via
>   the `notify` bridge; all mutations invalidate `['backends']`.
> - StatusTag color mapping unit-tested (green/red/gold + default fallback).

## Phase 4 — History (`/history`)

- [x] **4.1** Filter bar: RoutedTo `Select` (backend options:
  name tag + URL, clearable), User (prefilled from `access.userName`, ADMIN-only
  editable, persisted to sessionStorage `username`), QueryId, Source, Query button.
  **(parity: filter bar, sessionStorage, non-admin readonly)** **Gate.**
- [x] **4.2** `queryHistory` query → `POST /webapp/findQueryHistory` sending
  `userName`/`backendUrl`/`queryId`/`source`/`page`/`pageSize:15` (reconciliation
  #1–3); server-side pagination; filters reset to page 1. **(parity: pagination,
  filters)** **Gate.**
- [x] **4.3** Results `Table`: QueryId (link `{externalUrl}/ui/query.html?{queryId}`),
  RoutingGroup (sortable+filterable), Name (resolved via backendUrl→backend mapping),
  RoutedTo (external link), User, Source, QueryText (truncated + tooltip),
  SubmissionTime (zoned, sortable). Resolve `externalUrl` client-side from backend
  list; degrade to plain text if unresolved (Go gap #4). **(parity: all column
  items)** **Gate.**
- [x] **4.4** SQL modal: full query text, SQL syntax highlight + line numbers +
  word-wrap, copy-to-clipboard button (label → "Copied!"/"Copy Failed"), opened by
  clicking QueryText. **(parity: SQL modal)** **Gate.**

> **Phase 4 notes:**
> - Filter bar uses antd `Form` (`layout="inline"`) rather than react-hook-form —
>   no validation needed for a plain filter row, and antd Form keeps it terse. RHF
>   stays the form choice for create/edit dialogs (cluster, etc.).
> - Added **`highlight.js`** (`^11.11.1`) for SQL highlighting in the modal — the
>   only Phase-4 dependency change; commit the updated `package.json` + lockfile.
>   SQL is highlighted per-line so line numbers align with word-wrapped content.
> - `Name`/`RoutedTo`/QueryId-deeplink resolve the backend `externalUrl` client-side
>   from the backend list (`buildBackendMapping`, unit-tested) since Go leaves the
>   query record's `externalUrl` blank (gap #4); unresolved → plain text, no dead link.

## Phase 5 — Routing Rules (`/routing-rules`)

- [ ] **5.1** `routingRules` query → `getRoutingRules` (verb per router): loading
  `Spin`; 204/`isExternalRouting`→external notice; `[]`→empty notice. **(parity:
  loading, external notice, empty state)** **Gate.**
- [ ] **5.2** Rule cards (one antd `Card` per rule, title `Routing rule #N`): fields
  Name (always disabled), Description, Priority, Condition, Actions (TextArea,
  comma-joined display ↔ array on save). Independent per-card edit state; ADMIN-only
  Edit/Save. **(parity: cards, fields, actions, edit mode)** **Gate.**
- [ ] **5.3** Save → `POST /webapp/updateRoutingRules` (full rule); exit edit mode,
  update in-memory rule, success/error toast. **(parity: save, toasts)** **Gate.**

## Phase 6 — Final: polish, accessibility, tests, build, wiring

- [ ] **6.1** Accessibility pass: keyboard nav, focus management in modals,
  `jsx-a11y` clean, aria labels on icon-only buttons (theme/user/collapse).  **Gate.**
- [ ] **6.2** Test sweep: component tests covering each page's parity items; verify
  graceful-degradation paths for Go gaps #4/#5/#7/#8 (no broken links/crashes).
  **Gate.**
- [ ] **6.3** Verify all 60 PRD parity items (check the boxes); record any item that
  is manual-only with a note. **Gate.**
- [ ] **6.4** Production build: `pnpm build` → `dist/`; confirm assets resolve under
  `/trino-gateway/` base; size sanity check. **Gate.**
- [ ] **6.5** Wiring notes for the Go gateway embed: how `dist/` maps to the served
  static path (replacing `cmd/trino-goway/web/dist`), and the **SPA-fallback
  requirement** for browser-router deep routes (or the hash-router fallback). Hand
  the backend-dependency list (PRD §Backend Dependencies) to the team-lead.  **Gate.**

---

## Task counts

- Phase 0 — Scaffold: **6**
- Phase 1 — Core infra: **12**
- Phase 2 — Dashboard: **5**
- Phase 3 — Cluster: **6**
- Phase 4 — History: **4**
- Phase 5 — Routing Rules: **3**
- Phase 6 — Final: **5**
- **Total: 41 tasks**, each gated by the DoD.
