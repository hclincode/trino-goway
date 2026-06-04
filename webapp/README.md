# trino-goway Web UI (`webapp/`)

A modern **React 19** single-page admin UI for the [trino-goway](../README.md)
gateway. It is a ground-up rebuild of the original trino-gateway webapp
(Semi UI / React 18) on a current best-practice stack, and is built to a static
bundle served by the Go gateway under the `/trino-gateway/` base path.

## What it does

The UI is the operator console for the gateway. It provides five pages plus a shared
shell (collapsible sidebar, light/dark/auto theme, timezone selector, user menu):

| Page | Purpose |
|------|---------|
| **Login** | Form / OAuth (SSO) / no-auth sign-in, driven by the gateway's `loginType`; OIDC cookie hand-off; logout. |
| **Dashboard** | Summary card (9 live stats) + ECharts line chart (queries/min per backend) + doughnut (distribution), timezone-aware. |
| **Cluster** | Sortable/filterable backend table; active toggle; create/edit modal; delete confirm. Writes are ADMIN-gated. |
| **History** | Filterable, server-paginated query history; full-text SQL modal (syntax-highlighted + copy); query deep-links. |
| **Routing Rules** | One card per rule with independent inline edit; external-routing and empty states. |

Role gating (ADMIN / USER / API) and graceful degradation around known backend data
gaps are built in. See `docs/PRD.md` for scope and the 60-item parity checklist, and
`docs/CONVENTIONS.md` for the locked stack rationale.

## Stack

- **pnpm** (required — not npm/yarn) · **Vite 6** · **React 19** · **TypeScript (strict)**
- **React Router v7** (browser router, basename `/trino-gateway`)
- **TanStack Query v5** (server state) · **Zustand v5** (token + config, persisted)
- **Ant Design v5** (+ `@ant-design/icons`) — token theming, light/dark algorithm
- **ECharts** via `echarts-for-react` · **react-hook-form + zod** · **react-i18next** · **highlight.js**
- **Vitest + React Testing Library** · **ESLint (flat) + Prettier**

---

## 1. Prepare your environment

| Requirement | Notes |
|-------------|-------|
| **Node.js ≥ 20** | Developed on Node 26. Check with `node --version`. |
| **pnpm ≥ 9** | The package manager for this project — do **not** use npm or yarn. |

Install pnpm if you don't have it:

```bash
# Option A — via Corepack (bundled with Node)
corepack enable && corepack prepare pnpm@latest --activate

# Option B — global install
npm install -g pnpm

pnpm --version   # verify
```

## 2. Install dependencies

From this directory (`webapp/`):

```bash
pnpm install
```

This installs from the committed `pnpm-lock.yaml` for reproducible builds. (The first
install may ask to approve the `esbuild` build script; it is pre-approved in
`pnpm-workspace.yaml`.)

---

## 3. Usage guide

### Run the dev server

The UI talks to a running gateway through a dev proxy: the app calls `/api/...` and
Vite strips the `/api` prefix and forwards to the gateway's **admin** listener (where
the `/webapp/*`, `/gateway/*`, and auth endpoints live).

```bash
# Point the proxy at your gateway's admin listener (default admin port is 8090),
# then start the dev server with HMR:
VITE_BASE_URL=http://localhost:8090 pnpm dev
```

Then open the printed URL (e.g. `http://localhost:5173/trino-gateway/`).

Proxy environment variables (both optional):

| Var | Default | Meaning |
|-----|---------|---------|
| `VITE_BASE_URL` | `http://localhost:8080` | Origin of the running gateway. **Set this to your admin listener** (e.g. `:8090`) — that's where the webapp APIs are served. |
| `VITE_PROXY_PATH` | `/api` | Request prefix that is stripped before forwarding. |

> No gateway running? The pages will load but API calls will fail — start `trino-goway`
> first (see the repo `../README.md`), or point `VITE_BASE_URL` at any host serving the
> `/webapp/*` contract.

### Build for production

```bash
pnpm build      # tsc -b && vite build  →  dist/
pnpm preview    # serve the built dist/ locally to sanity-check
```

`dist/` contains the static bundle with all assets pathed under `/trino-gateway/`,
ready to be embedded and served by the Go gateway. See `docs/EMBED.md` for the
gateway wiring.

### Quality gates (Definition of Done)

Every change must pass all four gates before it is considered done:

```bash
pnpm typecheck   # tsc --noEmit (strict)
pnpm lint        # eslint . --max-warnings 0   (zero warnings)
pnpm test        # vitest run
pnpm build       # production build
```

### Other scripts

```bash
pnpm test:watch  # vitest in watch mode
pnpm lint:fix    # eslint --fix
pnpm format      # prettier --write .
```

---

## Project structure

```
webapp/
  src/
    api/         client.ts (envelope/token/401/204) + endpoints/ (per domain)
    app/         Providers, router, AuthGate, RouteGuard, queryClient, notify
    components/  reusable pieces (StatusTag, ExternalLink)
    context/     timezone provider + hook
    features/    one folder per page: auth, dashboard, cluster, history,
                 routingRules, home — each with its Page, queries, and parts
    hooks/       useTheme, useChartColors, useVisibleRoutes
    layout/      RootLayout, header pieces, sidebar, user menu, profile modal
    locales/     en_US resource + i18n bootstrap (typed)
    stores/      access (token+user) + config (theme), zustand persist
    types/       API types mirrored from the Go contract
    utils/       time (Intl-zoned), clipboard, cssVar
    test/        setup + renderWithProviders helper
  docs/          CONVENTIONS, PRD, TODO, EMBED, PARITY, studies/
  vite.config.ts, tsconfig*.json, eslint.config.js, package.json
```

## API contract & backend dependencies

The client wraps the gateway's `{code,msg,data}` envelope, attaches the JWT bearer
token, treats `204` on `getRoutingRules` as external-routing, and maps `401/403` to
session expiry. The frontend aligns to the Go gateway's actual field names
(`userName` / `backendUrl` / `pageSize`) and degrades gracefully around known
Go-side data gaps (blank query `externalUrl`, empty dashboard `lineChart`, absent
`disablePages`). These backend follow-ups are tracked in the gateway's
`../docs/TODO.md` (Phase 10) and `docs/PRD.md` → "API reconciliation".

## Documentation

- `docs/CONVENTIONS.md` — locked stack, layout, Definition of Done
- `docs/PRD.md` — goal, scope, parity checklist, API reconciliation
- `docs/TODO.md` — phased task breakdown (all phases complete)
- `docs/PARITY.md` — 60-item parity verification
- `docs/EMBED.md` — how the build maps into the Go gateway bundle
- `docs/studies/` — analysis of the original webapp
