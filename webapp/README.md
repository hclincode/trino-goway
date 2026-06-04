# trino-goway Web UI

A React 19 single-page admin UI for the trino-goway gateway. It rebuilds the
original trino-gateway webapp (Semi UI / React 18) on a modern stack and is built
to a static bundle served by the Go gateway under the `/trino-gateway/` base path.

## Stack

- **pnpm** (required — not npm/yarn) · **Vite 6** · **React 19** · **TypeScript strict**
- **React Router v7** (browser router, basename `/trino-gateway`)
- **TanStack Query v5** (server state) · **Zustand v5** (token + config, persisted)
- **Ant Design v5** (+ `@ant-design/icons`) — token theming, light/dark algorithm
- **ECharts** via `echarts-for-react` (dashboard line + doughnut)
- **react-hook-form + zod** (cluster create/edit form)
- **react-i18next** (en_US; pluggable) · **highlight.js** (SQL modal)
- **@vvo/tzdb** + native `Intl` (timezone-aware formatting)
- **Vitest + React Testing Library** · **ESLint flat + Prettier**

See `docs/CONVENTIONS.md` for the locked stack rationale and `docs/PRD.md` for scope.

## Prerequisites

- Node 20+ (developed on Node 26)
- pnpm 9+ (`corepack enable` or `npm i -g pnpm`)

## Getting started

```bash
pnpm install        # installs deps; commit pnpm-lock.yaml
pnpm dev            # Vite dev server (proxies /api → the Go gateway)
```

Dev proxy env (optional):

- `VITE_PROXY_PATH` — request prefix stripped before forwarding (default `/api`)
- `VITE_BASE_URL` — backend gateway origin (default `http://localhost:8080`)

The dev app calls `'/api/webapp/...'`; the proxy strips `/api` and forwards to the
gateway. In production the app calls the same paths same-origin under the base path.

## Scripts (Definition of Done)

Every change must pass all four gates before it is considered done:

```bash
pnpm typecheck      # tsc --noEmit (strict)
pnpm lint           # eslint . --max-warnings 0   (zero warnings)
pnpm test           # vitest run
pnpm build          # tsc -b && vite build → dist/
```

Other scripts: `pnpm preview` (serve the built bundle), `pnpm test:watch`,
`pnpm lint:fix`, `pnpm format`.

## Project structure

```
src/
  api/            client.ts (envelope/token/401/204) + endpoints/ (per domain)
  app/            Providers, router, AuthGate, RouteGuard, queryClient, notify
  components/     reusable pieces (StatusTag, ExternalLink)
  context/        timezone provider + hook
  features/       one folder per page: auth, dashboard, cluster, history,
                  routingRules, home — each with its Page, queries, and parts
  hooks/          useTheme, useChartColors, useVisibleRoutes
  layout/         RootLayout, header pieces, sidebar, user menu, profile modal
  locales/        en_US resource + i18n bootstrap (typed)
  stores/         access (token+user) + config (theme), zustand persist
  types/          API types mirrored from the Go contract
  utils/          time (Intl-zoned), clipboard, cssVar
  test/           setup + renderWithProviders helper
```

## Pages

- **Login** — form / oauth / no-auth (driven by `POST /loginType`); OIDC `token`
  cookie hand-off; logout.
- **Dashboard** — summary card (9 stats) + ECharts line + doughnut; timezone-aware.
- **Cluster** — sortable/filterable backend table; active toggle; create/edit modal;
  delete confirm; ADMIN-gated writes.
- **History** — filter bar + server-paginated table; SQL modal (highlight + copy);
  query deep-links; timezone-aware times.
- **Routing Rules** — card per rule with independent inline edit; external/empty states.

## API contract notes

The client wraps the gateway's `{code,msg,data}` envelope, attaches the JWT bearer
token, treats `204` on `getRoutingRules` as external-routing, and maps `401/403` to
session expiry. The frontend aligns to the Go gateway's actual field names
(`userName`/`backendUrl`/`pageSize`) and degrades gracefully around known Go-side
data gaps (blank query `externalUrl`, empty dashboard `lineChart`, absent
`disablePages`). See `docs/PRD.md` → "API reconciliation" and "Backend Dependencies".

## Building into the Go gateway

`pnpm build` emits `dist/` with all assets pathed under `/trino-gateway/`. See
`docs/EMBED.md` for how this maps into the gateway's embedded bundle (pairs with the
Go-side embed task).
