# Frontend Conventions — trino-goway web UI

**Date:** 2026-06-03
**Status:** Locked (governs all `webapp/` work)
**Owner:** frontend-architect

This document locks the stack, layout, naming, and the Definition of Done for the
rebuilt trino-gateway web UI. It is the source of truth: PRD and TODO defer to it.
Read it before writing any code. Stack changes require a team-lead decision and an
edit here first.

Inputs this is built on:
- `docs/studies/webapp-feature-inventory.md` — 60-item parity checklist (acceptance criteria)
- `docs/studies/webapp-api-and-data-model.md` — 13 endpoints + 8 API mismatches
- `docs/studies/webapp-architecture-stack-and-ux.md` — original stack, stores, auth, charts, i18n

---

## Stack (locked)

| Concern | Choice | One-line justification |
|---|---|---|
| Package manager | **pnpm** (latest) | Hard requirement. Strict, content-addressed store; `pnpm-lock.yaml` is committed. **npm/yarn are forbidden.** |
| Build / dev server | **Vite** (latest) | Original used Vite 5; fast HMR, first-class TS, simple static-bundle output for the Go embed. |
| UI framework | **React 19** | Latest stable; brings Actions, `use`, ref-as-prop, improved Suspense. |
| Language | **TypeScript** (latest), `strict: true` | Catch contract drift against the Go envelope at compile time. No implicit `any`. |
| Routing | **React Router v7** (data router, `createBrowserRouter`) | Latest. See "Router mode" below — we move from the original HashRouter to a **browser router under a base path**. |
| Server state | **TanStack Query v5** | Caching, request dedupe, background refetch, mutation invalidation — replaces the original's hand-rolled zustand fetch semaphores. |
| Client/UI state | **Zustand v5** | Matches original; small, persist-to-localStorage middleware for auth token + app config. |
| UI component library | **Ant Design (antd) v5** | See justification below — fastest near-mechanical path to parity. |
| Charts | **ECharts via `echarts-for-react`** | Matches original line + doughnut fidelity; theme-aware; the wrapper handles init/dispose/resize that the original omitted. |
| Forms | **react-hook-form + zod** (`@hookform/resolvers`) | Typed schemas, minimal re-renders; integrates with antd inputs via `Controller`. |
| Styling | **CSS Modules** + antd theming (`ConfigProvider` design tokens) | No Tailwind — antd tokens cover theme/dark-mode; CSS Modules for the handful of layout/shell rules. |
| i18n | **react-i18next** | en_US-only at launch but pluggable; keys live in locale files, no hardcoded UI strings. antd locale wired via `ConfigProvider locale`. |
| Date/timezone | **`@vvo/tzdb`** (IANA list) + **`date-fns` / `date-fns-tz`** | Replaces `moment` (deprecated). Zoned formatting for dashboard + history. |
| Data layer | Typed `fetch` client wrapping the `{code,msg,data}` envelope | Single choke point for JWT bearer header, envelope unwrap, 204→`isExternalRouting`, 401/403→session expiry. |
| Testing | **Vitest + React Testing Library** (jsdom) | Unit/component. **Playwright optional** for a thin e2e smoke later. |
| Lint / format | **ESLint flat config** (`typescript-eslint`, `eslint-plugin-react-hooks`, `jsx-a11y`) + **Prettier** | Flat config is the current standard; Prettier owns formatting, ESLint owns correctness. |

### UI library justification (antd v5 over Mantine v7)

The original app is built on Semi UI (ByteDance), which is an AntD-derived design
system: same `Table` column model, `Switch`, `Modal`, `Popconfirm`, `Tag`,
`Descriptions`, `Form`, `Toast`/`message`, `Select`, `Spin`, `Tooltip`,
`Dropdown`, `Layout`/`Header`/`Sider`/`Content`/`Nav`. Choosing **antd v5** makes
the rebuild a near-mechanical mapping component-for-component (e.g. Semi
`Popconfirm` → antd `Popconfirm`, Semi `Descriptions` → antd `Descriptions`, Semi
`Toast` → antd `message`/`notification`), which is the fastest route to the
60-item parity bar. antd v5 also ships token-based theming with a built-in dark
algorithm (`theme.darkAlgorithm`) that cleanly replaces Semi's
`theme-mode="dark"` body attribute, and exposes CSS-variable design tokens we read
for ECharts legend colors. Mantine v7 is the runner-up (excellent hooks, CSS-var
theming) but its table/popconfirm/descriptions story is less 1:1 with Semi, so it
would cost more bespoke work for no parity gain.

### Router mode: browser router under a base path (not hash)

The original uses `HashRouter` so the Go server only ever serves the index at one
path. We switch to **`createBrowserRouter` with `basename="/trino-gateway"`** and
Vite `base: '/trino-gateway/'`. Justification: cleaner URLs, native data-router
loaders/actions, and no `#` in deep links (history QueryId deep-links read better).
**Backend dependency (note for Go side):** the gateway must serve the SPA
`index.html` for unknown sub-paths under the base (SPA fallback), e.g.
`GET /trino-gateway/cluster` → `index.html`, so a hard refresh on a deep route
works. This is tracked as a backend dependency in `PRD.md`. If the Go fallback is
not in place by integration time, we fall back to `createHashRouter` (one-line
change) — the data-router API is identical.

---

## Data layer contract

A single typed client (`src/api/client.ts`) wraps `fetch`:

- **Envelope:** every `/webapp/*` and `/login*` response is `{ code, msg, data }`.
  The client returns `data` on `code === 200`, otherwise throws an `ApiError`
  carrying `code` + `msg`.
- **Auth header:** `Authorization: Bearer <token>` from the access store on every
  request; `Content-Language: en_US`.
- **204 special case:** `GET /webapp/getRoutingRules` may answer `204 No Content`
  → the client resolves to `{ isExternalRouting: true }`.
- **401/403:** clear the token in the access store and raise a session-expiry
  signal (toast + redirect to login). Centralized — components never special-case
  auth status.
- **Network failure:** surfaces the i18n string "The network has wandered off,
  try again later."
- **Base URL:** dev uses Vite proxy (`/api` → gateway, `/api` prefix stripped);
  prod calls same-origin under the base path. Configured via `import.meta.env`.

TanStack Query owns caching/refetch; the client is transport-only. Mutations
invalidate the relevant query keys (e.g. `saveBackend` → invalidate
`['backends']`).

---

## Directory layout

```
webapp/
  docs/                     # this doc, PRD.md, TODO.md, studies/
  public/                   # static assets copied verbatim (logo.svg, favicon)
  src/
    api/
      client.ts             # fetch wrapper: envelope, token, 204, 401/403
      endpoints/            # one file per domain: auth.ts, cluster.ts, history.ts,
                            #   dashboard.ts, routingRules.ts, uiConfig.ts
    app/
      router.tsx            # createBrowserRouter, route table, role/permission guards
      providers.tsx         # QueryClientProvider, ConfigProvider, I18nextProvider, ErrorBoundary
    components/             # reusable presentational pieces (StatusTag, ExternalLink, SqlModal…)
    features/              # one folder per page: login/ dashboard/ cluster/ history/ routingRules/
      <feature>/
        <Feature>Page.tsx
        components/         # page-local pieces
        queries.ts         # TanStack query/mutation hooks for this feature
        schema.ts          # zod schemas for this feature's forms
    layout/                 # RootLayout, Header, Sidebar, UserMenu, ProfileModal, ThemeToggle
    stores/                 # access.ts (token+user), config.ts (theme), zustand persist
    context/                # TimezoneContext (or a zustand slice)
    locales/                # en_US.json (+ i18n bootstrap)
    types/                  # shared TS types mirrored from the Go contract
    hooks/                  # cross-feature hooks (useTheme, useTimezone, useRole)
    utils/                  # time formatting, clipboard, css-var readers
    styles/                 # globals.css, *.module.css
    main.tsx                # entry
  index.html
  vite.config.ts
  tsconfig.json
  eslint.config.js          # flat config
  .prettierrc
  package.json
  pnpm-lock.yaml            # COMMITTED
  .gitignore
```

`features/` holds page-bound code; `components/` holds genuinely reusable pieces.
When in doubt, start in the feature folder and promote to `components/` on second use.

---

## Naming conventions

- **Components / classes / types:** `PascalCase` (`ClusterPage`, `BackendData`).
- **Files:** components `PascalCase.tsx`; everything else `camelCase.ts`
  (`client.ts`, `queries.ts`). CSS Modules `Name.module.css`.
- **Hooks:** `useThing`. **Zustand stores:** `useAccessStore`, `useConfigStore`.
- **TanStack query keys:** array form, domain-first: `['backends']`,
  `['queryHistory', filters]`, `['distribution']`, `['routingRules']`.
- **Types mirroring the Go contract** live in `src/types/` and use the JSON field
  names exactly (`externalUrl`, `routingGroup`, `captureTime`).
- **i18n keys:** `page.section.label` dot-namespaced; no hardcoded English in TSX.
- **Env vars:** `VITE_` prefix only (Vite requirement).

---

## Definition of Done (every task / iteration)

A task is **not** done until all four gates pass locally, in this order:

```bash
pnpm typecheck   # tsc --noEmit (strict)
pnpm lint        # eslint . --max-warnings 0 — zero errors AND zero warnings
pnpm test        # vitest run — all green
pnpm build       # vite build — produces dist/ with no errors
```

Required `package.json` scripts (exact names):

```jsonc
{
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview",
    "typecheck": "tsc --noEmit",
    "lint": "eslint . --max-warnings 0",
    "lint:fix": "eslint . --fix",
    "format": "prettier --write .",
    "test": "vitest run",
    "test:watch": "vitest"
  }
}
```

`pnpm build`'s `tsc -b` makes the build itself a type gate; `typecheck` stays as a
fast standalone gate for the inner loop. No task may merge with a failing gate or a
skipped test left silently skipped.

---

## Commit policy

- Small, single-purpose commits; one TODO task per commit where practical.
- Subject in imperative mood; body explains *why* when non-obvious.
- **Commit `pnpm-lock.yaml`** with any dependency change.
- Do not commit `dist/`, `node_modules/`, or local env files (see `.gitignore`).
- Never touch the `trino/` or `trino-gateway/` submodules (per repo `CLAUDE.md`).
- Commit/push only when the team-lead asks; branch off `main` first.
- End commit messages with the required co-author trailer.

---

## Team coordination

- Work is tracked in the shared task list. Each TODO phase/task maps to a task
  item; **claim it (set owner) and mark `in_progress` before starting, `completed`
  only when its DoD gate is green** — never mark complete with a failing gate.
- One person per feature folder at a time to avoid churn; cross-cutting changes
  (api client, stores, layout, providers) are coordinated through the team-lead.
- Backend (Go-side) dependencies discovered during a task are reported to the
  team-lead and cross-referenced in `PRD.md` → "API reconciliation"; do not edit Go
  code from the frontend team without an explicit hand-off.
- Surface blockers early via SendMessage to the team-lead rather than guessing at
  contract behavior.
