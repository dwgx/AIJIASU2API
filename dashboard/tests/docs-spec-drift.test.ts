// @ts-expect-error Node built-in; this project does not depend on @types/node.
import { execFileSync } from 'node:child_process'
// @ts-expect-error Node built-in; this project does not depend on @types/node.
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
// @ts-expect-error Node built-in; this project does not depend on @types/node.
import { tmpdir } from 'node:os'
// @ts-expect-error Node built-in; this project does not depend on @types/node.
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { docsPages } from '../src/docs/content'
import { reference } from '../src/docs/reference.generated'

declare const process: { cwd(): string; execPath: string }

/**
 * Drift guard for the public docs data layer.
 *
 * `docs/openapi.json` is the source of truth: the generated reference must
 * classify the same 120 paths / 148 operations, must not publish a closed
 * product tag, and must be byte-identical to a fresh generator run. The
 * Markdown mirror and `llms.txt` may not mention an admin-only path.
 */

interface SpecOperation {
  operationId: string
  tags?: string[]
  summary?: string
  'x-required-scope'?: string
  responses?: Record<string, unknown>
}

type SpecPathItem = Record<string, SpecOperation>

interface SpecDocument {
  info: { title: string; version: string }
  tags?: Array<{ name: string; description?: string }>
  paths: Record<string, SpecPathItem>
}

type Access = 'public' | 'scoped' | 'admin'

const METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options'] as const
const CLOSED_TAGS: Record<string, true> = {
  Portal: true,
  'Order Admin': true,
  'Tenant Admin': true,
  Payments: true,
}
const PUBLIC_OPERATIONS: Record<string, true> = {
  'GET /healthz': true,
  'GET /readyz': true,
}
const MIRROR_SLUGS = ['overview', 'authentication', 'scopes', 'errors', 'sdks', 'limits', 'reference']

const cwd = process.cwd()
const spec = JSON.parse(
  readFileSync(join(cwd, '..', 'docs', 'openapi.json'), 'utf8'),
) as SpecDocument

function classify(method: string, path: string, operation: SpecOperation): Access {
  if (PUBLIC_OPERATIONS[`${method.toUpperCase()} ${path}`] === true) return 'public'
  const scope = operation['x-required-scope']
  return scope && scope !== 'admin' ? 'scoped' : 'admin'
}

function specOperations(): Array<{ method: string; path: string; operation: SpecOperation; access: Access }> {
  const rows: Array<{ method: string; path: string; operation: SpecOperation; access: Access }> = []
  for (const [path, pathItem] of Object.entries(spec.paths)) {
    for (const method of METHODS) {
      const operation = pathItem[method]
      if (!operation) continue
      rows.push({ method: method.toUpperCase(), path, operation, access: classify(method, path, operation) })
    }
  }
  return rows
}

/** Paths whose every declared method is admin-only; the mirrors may not name them. */
function adminOnlyPaths(rows: Array<{ method: string; path: string; access: Access }>): Set<string> {
  const byPath = new Map<string, Access[]>()
  for (const row of rows) byPath.set(row.path, [...(byPath.get(row.path) ?? []), row.access])
  return new Set(
    [...byPath.entries()]
      .filter(([, accesses]) => accesses.every((access) => access === 'admin'))
      .map(([path]) => path),
  )
}

function normalizePathToken(raw: string): string {
  let token = raw.replace(/[.,;:)\]]+$/, '')
  if (token.endsWith('/')) token = token.slice(0, -1)
  return token.replace(/\/(?:\$[A-Za-z_][A-Za-z0-9_]*|\{[A-Za-z_]+\})$/, '/{id}')
}

const rows = specOperations()
const adminPaths = adminOnlyPaths(rows)

describe('docs/openapi.json classification', () => {
  it('matches the contract the docs were generated from', () => {
    expect(Object.keys(spec.paths)).toHaveLength(120)
    expect(rows).toHaveLength(148)
    expect(rows.filter((row) => row.access === 'public')).toHaveLength(2)
    expect(rows.filter((row) => row.access === 'scoped')).toHaveLength(54)
    expect(rows.filter((row) => row.access === 'admin')).toHaveLength(92)
  })

  it('agrees with the generated counts', () => {
    expect(reference.counts).toEqual({
      paths: 120,
      operations: 148,
      public: 2,
      scoped: 54,
      admin: 92,
      listed: 107,
      closed: 41,
    })
    expect(reference.operations).toHaveLength(107)
    expect(reference.operations.filter((operation) => operation.access === 'public')).toHaveLength(2)
    expect(reference.operations.filter((operation) => operation.access === 'scoped')).toHaveLength(54)
    expect(reference.operations.filter((operation) => operation.access === 'admin')).toHaveLength(51)
    expect(reference.adminTagCounts.reduce((sum, row) => sum + row.operations, 0)).toBe(92)
  })
})

describe('generated reference', () => {
  it('only lists paths that exist in the spec with the same method and operationId', () => {
    for (const operation of reference.operations) {
      const pathItem = spec.paths[operation.path]
      expect(pathItem, `${operation.method} ${operation.path}`).toBeDefined()
      const specOperation = pathItem[operation.method.toLowerCase()]
      expect(specOperation, `${operation.method} ${operation.path}`).toBeDefined()
      expect(specOperation.operationId).toBe(operation.operationId)
      expect(operation.access).toBe(
        classify(operation.method, operation.path, specOperation),
      )
    }
  })

  it('keeps closed product tags out of the public reference', () => {
    for (const tag of Object.keys(CLOSED_TAGS)) {
      expect(reference.tags.some((entry) => entry.name === tag)).toBe(false)
      expect(reference.operations.some((operation) => operation.tag === tag)).toBe(false)
      expect(reference.adminTagCounts.find((entry) => entry.tag === tag)?.closed).toBe(true)
    }
    const closedCounts = reference.adminTagCounts
      .filter((entry) => CLOSED_TAGS[entry.tag] === true)
      .reduce((sum, entry) => sum + entry.operations, 0)
    expect(closedCounts).toBe(41)

    // The names may exist only inside the admin appendix, never in an emitted
    // tag or operation description above it.
    const generated = readFileSync(join(cwd, 'src', 'docs', 'reference.generated.ts'), 'utf8')
    const appendixStart = generated.indexOf('"adminTagCounts"')
    expect(appendixStart).toBeGreaterThan(-1)
    const appendix = generated.slice(appendixStart)
    for (const tag of Object.keys(CLOSED_TAGS)) {
      const total = generated.split(`"${tag}"`).length - 1
      const inAppendix = appendix.split(`"${tag}"`).length - 1
      expect(total, tag).toBe(inAppendix)
    }
  })

  it('marks scoped, public and admin operations honestly', () => {
    for (const operation of reference.operations) {
      if (operation.access === 'public') {
        expect(PUBLIC_OPERATIONS[`${operation.method} ${operation.path}`]).toBe(true)
        expect(operation.scope).toBeNull()
      } else if (operation.access === 'scoped') {
        expect(operation.scope).toBeTruthy()
      } else {
        expect(operation.scope).toBeNull()
      }
    }
    for (const tag of reference.tags) {
      const rowsForTag = reference.operations.filter((operation) => operation.tag === tag.name)
      expect(tag.counts).toEqual({
        public: rowsForTag.filter((operation) => operation.access === 'public').length,
        scoped: rowsForTag.filter((operation) => operation.access === 'scoped').length,
        admin: rowsForTag.filter((operation) => operation.access === 'admin').length,
      })
    }
  })

  it('keeps the scoped account-device read under Accounts, not Lab', () => {
    const device = reference.operations.find(
      (operation) => operation.operationId === 'getAccountDevice',
    )
    expect(device?.access).toBe('scoped')
    expect(device?.scope).toBe('accounts:read')
    expect(device?.tag).toBe('Accounts')
    expect(
      reference.operations.some(
        (operation) => operation.tag === 'Lab' && operation.access !== 'admin',
      ),
    ).toBe(false)
    expect(reference.tags.find((tag) => tag.name === 'Lab')?.counts.scoped).toBe(0)
  })

  it('is current and deterministic', () => {
    expect(() =>
      execFileSync(process.execPath, ['scripts/gen-docs.mjs', '--check'], {
        cwd,
        encoding: 'utf8',
      }),
    ).not.toThrow()

    const root = mkdtempSync(join(tmpdir(), 'ajs-docs-'))
    try {
      const first = join(root, 'first')
      const second = join(root, 'second')
      execFileSync(process.execPath, ['scripts/gen-docs.mjs', '--out', first], { cwd })
      execFileSync(process.execPath, ['scripts/gen-docs.mjs', '--out', second], { cwd })
      const outputs = [
        'src/docs/reference.generated.ts',
        'public/llms.txt',
        ...MIRROR_SLUGS.map((slug) => `public/docs/${slug}.md`),
      ]
      for (const output of outputs) {
        expect(readFileSync(join(first, output), 'utf8'), output).toBe(
          readFileSync(join(second, output), 'utf8'),
        )
        expect(readFileSync(join(cwd, output), 'utf8'), output).toBe(
          readFileSync(join(first, output), 'utf8'),
        )
      }
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })
})

describe('Markdown mirror', () => {
  it('ships the six curated pages and the reference index', () => {
    expect(docsPages.map((page) => page.slug)).toEqual(MIRROR_SLUGS.slice(0, 6))
    for (const slug of MIRROR_SLUGS) {
      const file = join(cwd, 'public', 'docs', `${slug}.md`)
      expect(existsSync(file), file).toBe(true)
      const text = readFileSync(file, 'utf8')
      expect(text.startsWith('# '), slug).toBe(true)
      expect(text, slug).not.toMatch(/ajs_sk_[0-9a-f]{16,}/i)
    }
    for (const page of docsPages) {
      const text = readFileSync(join(cwd, 'public', 'docs', `${page.slug}.md`), 'utf8')
      expect(text.startsWith(`# ${page.title.en}\n`), page.slug).toBe(true)
    }
  })

  it('never names an admin-only path', () => {
    const tokenPattern = /\/v1\/[A-Za-z0-9_{}/.$-]+/g
    for (const slug of MIRROR_SLUGS) {
      const text = readFileSync(join(cwd, 'public', 'docs', `${slug}.md`), 'utf8')
      for (const match of text.matchAll(tokenPattern)) {
        const token = normalizePathToken(match[0])
        expect(spec.paths[token], `${slug}: ${match[0]}`).toBeDefined()
        expect(adminPaths.has(token), `${slug}: admin-only ${token}`).toBe(false)
      }
    }
  })

  it('lists no admin path and no credential in llms.txt', () => {
    const text = readFileSync(join(cwd, 'public', 'llms.txt'), 'utf8')
    expect(text).not.toMatch(/\/v1\//)
    expect(text).not.toMatch(/ajs_sk_[0-9a-f]{16,}/i)
    for (const path of adminPaths) {
      expect(text.includes(path), path).toBe(false)
    }
    for (const slug of MIRROR_SLUGS) {
      expect(text).toContain(`/docs/${slug}.md`)
    }
  })
})
