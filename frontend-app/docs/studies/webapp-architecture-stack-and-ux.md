---
title: Webapp Architecture, Stack, and UX
author: ui-analyst
role: UI Analyst
component: trino-gateway
topics: [webapp, frontend, architecture, auth, theming, i18n, charts]
date: 2026-06-03
status: draft
risk: medium
related-to: [webapp-feature-inventory.md, webapp-api-and-data-model.md]
---

# Webapp Architecture, Stack, and UX

## Summary

The original webapp is a React 18 + Vite 5 + TypeScript SPA built on Semi UI (ByteDance's design system) with Zustand for state, ECharts for charts, and react-router-dom v6 for routing. The rebuild can choose its own stack; this document inventories every Semi UI dependency, state management pattern, and UX behavior so the rebuild can faithfully re-implement them with different libraries.

---

## Tech Stack Versions

| Package | Version | Role |
|---|---|---|
| react / react-dom | 18.2.0 | UI framework |
| typescript | 5.9.3 | Type checking |
| vite | 5.4.21 | Build tool / dev server |
| @vitejs/plugin-react | 4.7.0 | Vite React plugin |
| vite-plugin-svgr | 4.5.0 | SVG-as-React-component imports |
| react-router-dom | 6.30.3 | Client-side routing |
| zustand | 4.5.7 | Global state management |
| @douyinfe/semi-ui | 2.96.1 | UI component library |
| @douyinfe/semi-icons | 2.96.0 | Icon library |
| @douyinfe/semi-icons-lab | 2.96.0 | Experimental icons |
| @douyinfe/semi-illustrations | 2.96.0 | Illustration assets |
| echarts | 5.4.3 | Charts (line chart, doughnut chart) |
| js-cookie | 3.0.5 | Read OIDC callback token from cookie |
| moment | 2.30.1 | Time duration formatting (used in `utils/time.ts`) |
| @vvo/tzdb | 6.198.0 | IANA timezone list for the timezone selector |
| sass | 1.99.0 | SCSS module styles |

**Build output base path:** `/trino-gateway/` (vite.config.ts:13). The Go server serves static assets under `/trino-gateway/assets/{*}` and the index at `/trino-gateway`.

**Dev proxy:** `VITE_PROXY_PATH` (default `/api`) is proxied to `VITE_BASE_URL`, and the leading `/api` prefix is stripped before forwarding to the backend (`vite.config.ts:16-22`). This means the dev app calls paths like `/api/webapp/getAllBackends` and the proxy strips `/api` before forwarding to the Go server.

---

## Routing

**Router type:** `HashRouter` (`src/App.tsx:7`). All routes use the hash fragment (`#/dashboard`, `#/cluster`, etc.). This means the index HTML is always served at `/trino-gateway` and the SPA handles routing client-side with no server-side path requirements.

**Route definitions** (`src/router.tsx:34-76`): an array of `RouterItem` objects that serve double duty as both react-router `<Route>` definitions and Semi UI `<Nav>` sidebar items. The `routeProps` field contains `{ path, element }` for the router and `items`/`text`/`icon` for the sidebar.

**Fallback route:** `path="*"` renders the `Home` component — a centered idle illustration with "Welcome, {username}" greeting.

**Landing redirect:** `/` → `/dashboard`.

---

## State Management (Zustand)

### Access store (`src/store/access.ts`, key: `"access-control"`)

Persisted to `localStorage` via `zustand/middleware/persist`.

| Field | Type | Description |
|---|---|---|
| `token` | string | JWT Bearer token |
| `userId` | string | from `/userinfo` |
| `userName` | string | from `/userinfo`; used as default History user filter |
| `nickName` | string | from `/userinfo` |
| `userType` | string | from `/userinfo` |
| `email` | string | from `/userinfo` |
| `phonenumber` | string | from `/userinfo` |
| `sex` | string | from `/userinfo` |
| `avatar` | string | from `/userinfo`; URL or data-URI for avatar image |
| `permissions` | string[] | page-level permission keys; empty = all allowed |
| `roles` | string[] | e.g. `["ADMIN"]`; drives ADMIN-gated UI |

Key methods:
- `updateToken(token)`: sets token, triggers `getUserInfo(true)` to hydrate user profile.
- `isAuthorized()`: returns `!!token`; also lazily calls `getUserInfo()` if not yet fetched.
- `getUserInfo(force?)`: calls `POST /userinfo`; de-duplicated by `fetchState` semaphore (0=not fetched, 1=fetching, 2=done).
- `hasRole(role)`: checks `roles.includes(role)`.
- `hasPermission(key)`: returns true if `permissions` is empty or includes the key.

### Config store (`src/store/config.ts`, key: `"app-config"`)

Persisted to `localStorage`.

| Field | Type | Default | Description |
|---|---|---|---|
| `avatar` | string | `"/trino-gateway/logo.svg"` | fallback avatar |
| `theme` | `"auto"` \| `"dark"` \| `"light"` | `"auto"` | current theme |
| `fontSize` | number | 14 | (not currently used in components) |
| `sidebarWidth` | number | 270 | (not currently used; layout hardcodes 240px/60px) |

Key methods:
- `update(updater)`: accepts an updater function that mutates a config copy, then stores it.
- `reset()`: restores all defaults.

---

## Auth and Login Flow

### Form login
1. `POST /loginType` → `"form"`: renders username + password form.
2. User submits → `POST /login` → receives `{ token }`.
3. Token stored in access store (persisted to localStorage).
4. `getUserInfo()` called → `POST /userinfo` → user profile merged into access store.
5. `App.tsx` re-renders: `access.isAuthorized()` now true → shows layout + routes.

### OAuth2 / OIDC login
1. `POST /loginType` → `"oauth"`: renders single SSO button.
2. User clicks → `POST /sso` → receives redirect URL string.
3. `window.location.href = redirectUrl` → browser goes to IdP.
4. After IdP callback: Go `GET /oidc/callback` handler sets a `token` cookie.
5. Browser redirected back to the SPA (at `/trino-gateway`).
6. On mount, `App.tsx` checks `Cookies.get('token')`, stores it in access store, removes cookie.

### No-auth mode
1. `POST /loginType` → `"none"`: renders form with password field read-only.
2. User submits with just username → `POST /login` → receives token.
3. Same flow as form login from step 3.

### Token storage
- Stored in `localStorage` via zustand persist (key: `"access-control"`).
- Sent as `Authorization: Bearer <token>` header on all API requests (`base.ts:148-153`).
- On 401/403: token cleared from store, expiry toast shown, request thrown.

### Session management
- No automatic token refresh.
- Expiry detected on any API call returning 401/403.
- Logout: `POST /logout` → clears token → `isAuthorized()` returns false → login screen shown.

---

## Semi UI Usage

The original app depends heavily on Semi UI (`@douyinfe/semi-ui`). The rebuild may replace this with any modern component library (Tailwind UI, shadcn/ui, Ant Design, etc.). Below is the inventory of all Semi UI components used so the rebuild knows what to match functionally.

| Component | Used in | Purpose |
|---|---|---|
| `Layout`, `Header`, `Sider`, `Content` | layout | App shell structure |
| `Nav` (horizontal + vertical) | layout | Header nav + sidebar nav with collapse |
| `Avatar` | layout | User profile modal avatar |
| `Dropdown`, `Dropdown.Menu`, `Dropdown.Item` | layout | User dropdown menu |
| `Modal` | layout, cluster, history, routing-rules | Dialogs |
| `Tag` | layout, cluster, history | Role tags, routing group tags, status tags |
| `Button`, `ButtonGroup` | layout, cluster, history, routing-rules | Actions |
| `Toast` | all | Success/error notifications |
| `Form`, `Form.Input`, `Form.Select`, `Form.Switch`, `Form.TextArea` | cluster, history, login, routing-rules | All forms |
| `Popconfirm` | cluster | Delete confirmation |
| `Switch` | cluster | Active toggle |
| `Table`, `Column` | cluster, history | Data tables |
| `Card` | dashboard, cluster, history, routing-rules | Content containers |
| `Row`, `Col` | dashboard | Grid layout |
| `Descriptions` | dashboard | Summary key-value pairs |
| `Tooltip` | dashboard | QPH/QPM/QPS help text |
| `Spin` | routing-rules, login | Loading indicator |
| `Typography.Text` | cluster, history | Links, truncated text |
| `CodeHighlight` | history | SQL syntax highlighting with line numbers |
| `Select`, `Select.Option` | TimezoneContext | Timezone dropdown |
| `Empty` | App.tsx | 404/idle page illustration |
| `LocaleProvider` | App.tsx | Semi UI locale |
| `IllustrationIdle`, `IllustrationIdleDark` | App.tsx | Idle page illustrations |

**Semi UI-specific behaviors the rebuild must replicate:**
- `theme-mode="dark"` attribute on `<body>` activates dark mode for Semi components.
- `<LocaleProvider locale={semi_en_US}>` sets component-level i18n (datepicker labels, etc.).
- Dark-mode-aware colors via CSS variables (`--semi-color-text-0`, `--semi-color-bg-0`, `--semi-color-nav-bg`, etc.).

---

## Theming and Dark Mode

Theme state cycles: `auto` → `light` → `dark` (persisted in config store).

`useSwitchTheme()` hook (`App.tsx:84-121`):
- `dark`: adds `class="dark"` and `theme-mode="dark"` to `<body>`.
- `light`: adds `class="light"`, removes `theme-mode` attribute.
- `auto`: reads `prefers-color-scheme` media query; sets `theme-mode` accordingly.
- Also updates `<meta name="theme-color">` for mobile browser chrome.

ECharts legend color update (`dashboard.tsx:129-143`): uses `MutationObserver` on `<body>` watching `theme-mode` attribute. When it changes, reads `getCSSVar('--semi-color-text-0')` and re-sets ECharts option.

Custom SCSS (`src/styles/globals.scss`):
- `--login-shadow` CSS variable differs between light and dark.
- `--semi-color-bg-0` overridden in light mode to `rgba(245,245,245,1)`.
- Font: `Inter, system-ui, Avenir, Helvetica, Arial, sans-serif`.

---

## Charts (ECharts)

Both charts are initialized via `echarts.init(refElement)` in a `useEffect` and configured with `chartInstance.setOption(option)`.

### Line chart (dashboard, 2/3 width, 450px height)

Data flow:
1. `distributionDetail.lineChart` = `Record<string, LineChartData[]>` — keyed by backend name.
2. All timestamps are extracted, min/max found, bucketed into 1-minute slots.
3. X-axis: minute labels formatted in the selected timezone (`formatZonedTimestamp(epochMs, tz)`).
4. One series per backend key; data = query counts per minute slot (0 for empty slots).
5. `type: 'line'`, `smooth: true`.

Configuration highlights:
- `xAxis.type: 'category'`
- `yAxis.minInterval: 1` (integer only)
- `tooltip.trigger: 'axis'` (crosshair across all series)
- `legend.textStyle.color` = CSS var `--semi-color-text-0`

### Doughnut chart (dashboard, 1/3 width, 450px height)

Data flow:
1. `distributionDetail.distributionChart` = `DistributionChartData[]`.
2. Each entry becomes a pie slice: `{ value: queryCount, name: backendName }`.

Configuration highlights:
- `series.type: 'pie'`, `radius: ['40%', '70%']` (donut hole)
- `itemStyle.borderRadius: 10`, `borderWidth: 2`, `borderColor: '#fff'`
- `label.show: false` (hidden by default)
- `emphasis.label.show: true`, `fontSize: 17`, `fontWeight: 'bold'` (shown on hover)
- `labelLine.show: false`
- `tooltip.trigger: 'item'`

**Note:** neither chart uses `chartInstance.resize()` on window resize. If the layout changes, charts may not fill their containers until re-mounted.

---

## i18n

Only English (`en_US`) locale is implemented. The locale system supports additional languages via the `ALL_LANGS` map (`src/locales/index.ts:9-11`), but none are added.

**Language detection order:**
1. `localStorage.getItem('lang')` — user-set preference.
2. `navigator.language.toLowerCase()` — browser locale.
3. Default: `"en_US"`.

**Fallback merge:** `merge(fallbackLang, targetLang)` deep-merges target locale into fallback. If a translation key is missing in the target, the en_US string is used.

**Semi UI locale:** set via `<LocaleProvider locale={semi_en_US}>` in `App.tsx:26`.

**`Content-Language` header** sent on every request: `getServerLang()` returns `"en_US"` (`base.ts:147`).

---

## Timezone Handling

**Context:** `TimezoneContext` (`src/components/TimezoneContext.tsx`) provides `{ timezone, changeTimezone }` to all children.

**Default:** `Intl.DateTimeFormat().resolvedOptions().timeZone ?? 'UTC'` — browser's local timezone.

**Timezone list:** all IANA timezone names from `@vvo/tzdb` (`getTimeZones({ includeUtc: true })`), sorted alphabetically. The dropdown is in the header, visible on all pages.

**Not persisted:** timezone selection is in-memory only (`useState`). Refreshing the page resets to browser timezone.

**Usage:**
- Dashboard: line chart x-axis labels (`formatZonedTimestamp(epochMs, tz)`), summary "Started at" (`formatZonedDateTime(isoString, tz)`).
- History: SubmissionTime column (`formatTimestamp(epochMs, tz)`).

---

## UX Details

### Sidebar collapse
- Collapsed width: 60px (icon-only); expanded: 240px.
- Transition: `transition: margin-left 0.3s ease` on the content area (`layout.module.scss:76`).
- Collapse toggle button at sidebar bottom (chevron left / chevron right icon).
- State: local `useState`, not persisted.

### Active navigation
- Selected sidebar item tracked by `selectedKey` state, synced to `location.pathname` on route change.
- Uses Semi UI `Nav` `selectedKeys` + `defaultSelectedKeys` props.

### User profile modal
- Triggered from user dropdown "Profile" item.
- 400×400, no cancel/close button (only the modal's built-in X).
- Shows: avatar (large, 72px), username (bold 20px), userId (with ID card icon), roles as colored tags.

### External links
- ProxyToUrl, ExternalUrl (cluster), RoutedTo (history), QueryId links: all open in `target="_blank"` with `underline` style.

### Confirmations
- Delete backend: `Popconfirm` (inline popover, not a modal).

### Copy to clipboard
- History SQL modal: `navigator.clipboard.writeText()` with fallback to `document.execCommand('copy')` (`utils/utils.ts:9-27`).
- Button label changes: "Copy to Clipboard" → "Copied!" or "Copy Failed" on result.

### sessionStorage for user filter
- History page persists `{ user }` filter to `sessionStorage` key `'username'` (`history.tsx:51-52`).
- On page load, the filter is initialized from sessionStorage if present, defaulting to the logged-in username.

### Error boundary
- `ErrorBoundary` class component wraps the entire app.
- On render error: shows "Oops, something went wrong!" with error message and component stack.
- Does not attempt recovery.

### Empty/idle page (404)
- `Home` component renders `IllustrationIdle` (light) / `IllustrationIdleDark` (dark) at 250×250.
- Description: "Welcome, {userName} 🌻🌻🌻".
- Margin: 100px.
