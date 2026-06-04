# Go-Embed / Build Wiring Plan

**Date:** 2026-06-04
**Owner:** frontend-architect
**Status:** Plan (docs only — the Go wiring itself is the Go team's task; pairs with Go TODO Phase 10 Task 65)

How the `webapp` production bundle maps into the trino-goway gateway's
embedded static UI. This is a plan for the Go side; no Go code is changed by the
frontend team.

---

## Current Go state (verified)

- `cmd/trino-goway/main.go:39` embeds the bundle: `//go:embed all:web/dist` →
  `var webDistFS embed.FS`, sub-FS'd to `web/dist` at `main.go:153` (`adminUIFS`,
  currently unused — `_ = adminUIFS`).
- `cmd/trino-goway/web/dist/` today holds only a placeholder `index.html` + `.gitkeep`.
- Routes (`internal/admin/router.go`):
  - `GET /` → 303 redirect to `/trino-gateway` (`:92-94`)
  - `GET /trino-gateway` → `serveIndex` (placeholder HTML, `:102`, `:200`)
  - `GET /trino-gateway/logo.svg` → `serveLogoSVG` (placeholder SVG, `:103`, `:206`)
  - `GET /trino-gateway/assets/{*}` → `serveAssets` (currently `http.NotFound`, `:104`, `:213`)
- The webapp endpoints (`/webapp/*`, `/login*`, `/userinfo`, `/sso`, `/logout`,
  `/oidc/callback`) are already registered.

## What the frontend produces

`pnpm build` (`tsc -b && vite build`) emits `dist/` with `base: '/trino-gateway/'`:

```
dist/
  index.html                 # references /trino-gateway/assets/* and /trino-gateway/logo.svg
  logo.svg
  assets/
    index-<hash>.js          # entry
    react-<hash>.js          # vendor: react, react-dom, react-router
    antd-<hash>.js           # vendor: antd + icons
    echarts-<hash>.js        # vendor: echarts
    query-<hash>.js          # vendor: tanstack-query + zustand
    <Page>-<hash>.js         # one lazy chunk per route
    *.css
```

All asset URLs are absolute under `/trino-gateway/`, matching the gateway's route
prefix — so they drop in with no rewriting.

## Mapping plan (Go side)

1. **Copy `webapp/dist/` → `cmd/trino-goway/web/dist/`** at Go build time
   (replacing the placeholder). Two options:
   - **Build step (recommended):** a Make/script target runs
     `pnpm -C webapp install --frozen-lockfile && pnpm -C webapp build`
     then `rm -rf cmd/trino-goway/web/dist && cp -r webapp/dist cmd/trino-goway/web/dist`,
     run before `go build`. The existing `//go:embed all:web/dist` then bundles it.
   - **Checked-in artifact (alternative):** commit the built `dist/` under
     `web/dist`. Simpler CI, but couples the bundle to git; not recommended.

2. **Wire the three handlers to the embedded FS** (replace the placeholders):
   - `serveAssets` → serve `adminUIFS` under `/trino-gateway/assets/*` with
     `http.FileServer(http.FS(...))` (long cache headers; filenames are hashed).
   - `serveIndex` → write `index.html` from `adminUIFS`.
   - `serveLogoSVG` → serve `logo.svg` from `adminUIFS` (or fold into the asset
     server and drop the dedicated route).

3. **Add a SPA fallback (required for the browser router).** The frontend uses
   `createBrowserRouter` with `basename="/trino-gateway"`, so a hard refresh or
   direct visit to e.g. `/trino-gateway/cluster` must return `index.html` (not 404).
   Add a catch-all `GET /trino-gateway/*` (after the `assets/*` and `api/*` routes)
   that:
   - serves the requested file from `adminUIFS` if it exists, else
   - serves `index.html` (SPA fallback).
   Keep it scoped under `/trino-gateway/` so it never shadows `/webapp/*`, `/login*`,
   `/oidc/callback`, `livez/readyz`, or the `/trino-gateway/api/*` legacy routes.

   **Fallback if the Go SPA route is not added:** the frontend can switch to
   `createHashRouter` (one-line change in `src/app/router.tsx`) — deep links become
   `/trino-gateway#/cluster`, which need no server route. Documented in
   `docs/CONVENTIONS.md`.

4. **Content types / caching:** `http.FileServer` sets content types from
   extensions. Recommend `Cache-Control: public, max-age=31536000, immutable` for
   `/assets/*` (hashed) and `no-cache` for `index.html`.

## Verification checklist (post-wiring, Go side)

- `GET /trino-gateway` returns the built `index.html`.
- `GET /trino-gateway/assets/index-<hash>.js` returns the JS with a JS content type.
- `GET /trino-gateway/logo.svg` returns the real logo.
- `GET /trino-gateway/cluster` (deep link, no client nav) returns `index.html`
  (SPA fallback) — app boots and routes client-side to the cluster page.
- `/webapp/*`, `/login*`, `/oidc/callback`, `livez`/`readyz` still resolve to their
  handlers (not shadowed by the catch-all).

## Notes

- The frontend's dev proxy (`/api` → gateway, prefix stripped) is dev-only; the
  embedded prod bundle calls the gateway same-origin under `/trino-gateway` and the
  `/webapp`, `/login`, etc. paths directly. No proxy in production.
- Bundle size: antd (~1.1 MB) and echarts (~1.05 MB) are the two large vendor
  chunks, isolated for long-term caching; gzipped ~350 KB each and loaded lazily
  (echarts only on the dashboard route).
