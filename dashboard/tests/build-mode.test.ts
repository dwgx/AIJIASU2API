// @ts-expect-error Node built-ins; this project does not depend on @types/node.
import { spawnSync } from 'node:child_process'
// @ts-expect-error Node built-ins; this project does not depend on @types/node.
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs'
// @ts-expect-error Node built-ins; this project does not depend on @types/node.
import { tmpdir } from 'node:os'
// @ts-expect-error Node built-ins; this project does not depend on @types/node.
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'

declare const process: {
  cwd(): string
  execPath: string
  env: Record<string, string | undefined>
}

/**
 * Build-mode contract tests for `vite.config.ts`.
 *
 * These spawn the real Vite CLI with temporary outDirs instead of calling the
 * guard function directly, so they prove what a release engineer observes:
 * a bare production build exits non-zero and writes no assets, the live recipe
 * emits a live manifest, and only `--mode mock` may produce a demo bundle.
 * The acceptance run separately exercises `npm run build`, `build:mock` and a
 * bare `npm run build` through the package scripts.
 */

const dashboardDir = process.cwd()
const tempRoots: string[] = []

interface BuildOutcome {
  status: number | null
  stdout: string
  stderr: string
  outDir: string
}

afterEach(() => {
  for (const root of tempRoots.splice(0)) {
    rmSync(root, { recursive: true, force: true })
  }
})

function runBuild(options: { mode?: string; env?: Record<string, string> } = {}): BuildOutcome {
  const root = mkdtempSync(join(tmpdir(), 'ajs-build-mode-'))
  tempRoots.push(root)
  const outDir = join(root, 'dist')

  const env: Record<string, string | undefined> = { ...process.env, NODE_ENV: 'production' }
  delete env.VITE_USE_MOCK
  delete env.VITE_PANEL_TOKEN
  delete env.VITE_API_BASE
  Object.assign(env, options.env)

  const args = [
    join(dashboardDir, 'node_modules', 'vite', 'bin', 'vite.js'),
    'build',
    '--outDir',
    outDir,
    '--emptyOutDir',
  ]
  if (options.mode) args.push('--mode', options.mode)

  const result = spawnSync(process.execPath, args, {
    cwd: dashboardDir,
    env,
    encoding: 'utf8',
    timeout: 120_000,
  })

  return {
    status: result.status,
    stdout: result.stdout ?? '',
    stderr: result.stderr ?? '',
    outDir,
  }
}

function emittedAssets(outDir: string): string[] {
  return existsSync(outDir) ? readdirSync(outDir) : []
}

describe('build modes', () => {
  it(
    'refuses a production build with VITE_USE_MOCK unset and emits no assets',
    () => {
      const build = runBuild()

      expect(build.status).not.toBe(0)
      expect(build.stderr).toContain('VITE_USE_MOCK=false')
      expect(build.stderr).toContain('build:mock')
      expect(emittedAssets(build.outDir)).toEqual([])
    },
    120_000,
  )

  it(
    'refuses a production build with VITE_USE_MOCK=true and emits no assets',
    () => {
      const build = runBuild({ env: { VITE_USE_MOCK: 'true' } })

      expect(build.status).not.toBe(0)
      expect(build.stderr).toContain('VITE_USE_MOCK=false')
      expect(emittedAssets(build.outDir)).toEqual([])
    },
    120_000,
  )

  it(
    'builds a live manifest for VITE_USE_MOCK=false',
    () => {
      const build = runBuild({ env: { VITE_USE_MOCK: 'false' } })

      expect(build.stderr).not.toContain('Refusing a demo bundle')
      expect(build.status).toBe(0)
      expect(emittedAssets(build.outDir)).toContain('index.html')
      const manifest = JSON.parse(readFileSync(join(build.outDir, 'ajs-build.json'), 'utf8'))
      expect(manifest).toEqual({
        schemaVersion: 1,
        mode: 'live',
        mock: false,
        apiBase: '',
        panelToken: false,
      })
    },
    120_000,
  )

  it(
    'accepts the explicit mock mode and labels the bundle as the demo',
    () => {
      const build = runBuild({ mode: 'mock' })

      expect(build.status).toBe(0)
      expect(emittedAssets(build.outDir)).toContain('index.html')
      const manifest = JSON.parse(readFileSync(join(build.outDir, 'ajs-build.json'), 'utf8'))
      expect(manifest).toEqual({
        schemaVersion: 1,
        mode: 'mock',
        mock: true,
        apiBase: '',
        panelToken: false,
      })
    },
    120_000,
  )

  it(
    'refuses VITE_PANEL_TOKEN in a live build',
    () => {
      const build = runBuild({ env: { VITE_USE_MOCK: 'false', VITE_PANEL_TOKEN: 'inline-me' } })

      expect(build.status).not.toBe(0)
      expect(build.stderr).toContain('VITE_PANEL_TOKEN')
      expect(emittedAssets(build.outDir)).toEqual([])
    },
    120_000,
  )

  it(
    'refuses VITE_PANEL_TOKEN in the explicit mock mode too',
    () => {
      const build = runBuild({ mode: 'mock', env: { VITE_PANEL_TOKEN: 'inline-me' } })

      expect(build.status).not.toBe(0)
      expect(build.stderr).toContain('VITE_PANEL_TOKEN')
      expect(emittedAssets(build.outDir)).toEqual([])
    },
    120_000,
  )

  it('keeps build:mock as the only demo recipe', () => {
    const pkg = JSON.parse(readFileSync(join(dashboardDir, 'package.json'), 'utf8'))
    const scripts: Record<string, string> = pkg.scripts

    expect(scripts.build).toBe('tsc -b && vite build')
    expect(scripts['build:mock']).toBe('tsc -b && vite build --mode mock')
    expect(Object.entries(scripts).filter(([, command]) => command.includes('--mode mock'))).toEqual([
      ['build:mock', 'tsc -b && vite build --mode mock'],
    ])
  })
})
