import react from '@vitejs/plugin-react'
import { loadEnv, type Plugin } from 'vite'
import { defineConfig } from 'vitest/config'

/**
 * Production build guard and build manifest.
 *
 * `VITE_USE_MOCK` is mock-first at runtime: anything except the exact string
 * "false" selects the in-browser demo data. That default is useful for `vite`
 * dev and Vitest, but it must never reach a release artifact, so a build fails
 * closed unless it is either an explicit demo (`--mode mock`, the
 * `npm run build:mock` recipe) or a live build (`VITE_USE_MOCK=false`, the
 * `npm run build` release path). A bare `npm run build` therefore exits
 * non-zero before any asset is written instead of silently emitting the demo.
 *
 * Every successful build records its resolved mode in `ajs-build.json`.
 * `deploy/production/build-bundles.sh` and `deploy/package.sh` refuse to
 * package a bundle whose manifest is not `mode: live`, `mock: false`,
 * `panelToken: false`; the production kit recipe additionally requires
 * `apiBase: ""` (same origin).
 *
 * A non-empty VITE_PANEL_TOKEN is a build error in every mode: Vite inlines
 * every VITE_* value into public JavaScript, so a token-bearing bundle must
 * never exist.
 */
function productionBuildGuard(mode: string, env: Record<string, string>): Plugin {
  return {
    name: 'ajs-production-build-guard',
    apply: 'build',
    configResolved() {
      if (env.VITE_PANEL_TOKEN) {
        throw new Error(
          'VITE_PANEL_TOKEN is set for a bundle build; Vite would inline the admin bearer into public JavaScript. Unset it and let nginx inject the bearer.',
        )
      }
      if (mode !== 'mock' && env.VITE_USE_MOCK !== 'false') {
        throw new Error(
          `Refusing a demo bundle: a ${mode} build requires VITE_USE_MOCK=false. ` +
            'Run `VITE_USE_MOCK=false npm run build` for a live bundle, or `npm run build:mock` for the in-browser demo.',
        )
      }
    },
    generateBundle() {
      const mock = env.VITE_USE_MOCK !== 'false'
      const manifest = {
        schemaVersion: 1,
        mode: mock ? 'mock' : 'live',
        mock,
        apiBase: env.VITE_API_BASE ?? '',
        panelToken: Boolean(env.VITE_PANEL_TOKEN),
      }
      this.emitFile({
        type: 'asset',
        fileName: 'ajs-build.json',
        source: `${JSON.stringify(manifest, null, 2)}\n`,
      })
    },
  }
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  return {
    plugins: [react(), productionBuildGuard(mode, env)],
    server: {
      host: '127.0.0.1',
      port: 5173,
    },
    preview: {
      host: '127.0.0.1',
      port: 4173,
    },
    test: {
      environment: 'jsdom',
      globals: true,
      setupFiles: './tests/setup.ts',
      css: true,
      restoreMocks: true,
      // Default 5 s is too tight for the app-shell tests under full parallel
      // load on HMC; the tests themselves complete, so give them headroom
      // instead of shrinking coverage.
      testTimeout: 15_000,
    },
  }
})
