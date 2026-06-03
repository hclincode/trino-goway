/// <reference types="vite/client" />
/// <reference types="vite-plugin-svgr/client" />

interface ImportMetaEnv {
  /** Dev-only proxy prefix stripped before forwarding to the gateway. */
  readonly VITE_PROXY_PATH?: string;
  /** Backend gateway origin used by the dev proxy. */
  readonly VITE_BASE_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
