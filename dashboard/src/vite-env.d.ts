/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_USE_MOCK?: string
  readonly VITE_API_BASE?: string
  readonly VITE_PANEL_TOKEN?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
