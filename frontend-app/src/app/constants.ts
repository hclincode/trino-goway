/**
 * Base path the Go gateway serves the bundle under. Keep in sync with Vite
 * `base` in vite.config.ts. Browser router requires the gateway to SPA-fallback
 * unknown sub-paths to index.html (see docs/PRD.md backend dependency).
 */
export const BASENAME = '/trino-gateway';
