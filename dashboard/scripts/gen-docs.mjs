// Generate the public docs data layer from docs/openapi.json.
//
//   node scripts/gen-docs.mjs            write src/docs/reference.generated.ts,
//                                        public/llms.txt and public/docs/*.md
//   node scripts/gen-docs.mjs --check    compare the outputs on disk, exit 1 on drift
//   node scripts/gen-docs.mjs --out DIR  write the same relative layout under DIR
//
// The generator is deterministic: no timestamps, no locale-dependent sorting,
// and one fixed key order per emitted object. `--check` is what the drift test
// runs, so a changed contract or a hand-edited output fails the suite.
//
// Classification (mirrors docs/API-GUIDE.md):
//   public  GET /healthz, GET /readyz
//   scoped  x-required-scope present and not "admin"
//   admin   everything else
// Closed product tags are omitted from the reference rows and kept only in the
// admin count appendix. Curated pages live in src/docs/content.ts so the app and
// the Markdown mirror render one source.

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const dashboardDir = resolve(scriptDir, '..')
const repoDir = resolve(dashboardDir, '..')
const specPath = join(repoDir, 'docs', 'openapi.json')
const contentPath = join(dashboardDir, 'src', 'docs', 'content.ts')

const CLOSED_TAGS = ['Portal', 'Order Admin', 'Tenant Admin', 'Payments']
const PUBLIC_OPERATIONS = { 'GET /healthz': true, 'GET /readyz': true }
// `Lab` is the admin telemetry surface. Its only scoped operation is
// `GET /v1/accounts/{id}/device` (accounts:read), which belongs with Accounts
// in the public reference; admin Lab rows keep their tag.
const SCOPED_TAG_REMAP = { Lab: 'Accounts' }
// The documented closed enum from docs/API-GUIDE.md, plus reserved FROZEN.
// A response description may name a store reason (for example the device-pool
// DEVICE_IMPORT_CONFLICT); only a documented code is emitted as `code`.
const ERROR_CODES = [
  'INVALID_ARGUMENT',
  'NOT_FOUND',
  'CONFLICT',
  'UPSTREAM_UNAVAILABLE',
  'QUOTA_EXCEEDED',
  'RATE_LIMITED',
  'UNAUTHORIZED',
  'FORBIDDEN',
  'INVITE_EXPIRED',
  'INVITE_REDEEMED',
  'METHOD_NOT_ALLOWED',
  'INTERNAL',
  'BACKUP_BUSY',
  'BACKUP_LIVE_NODE',
  'BACKUP_DAEMON_RUNNING',
  'BACKUP_UNAVAILABLE',
  'CURSOR_EXPIRED',
  'OPERATION_TIMEOUT',
  'PRECONDITION_FAILED',
  'PROVIDER_REFUND_UNSUPPORTED',
  'CONFIRMATION_REQUIRED',
  'FROZEN',
]
const METHOD_ORDER = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options']

const args = process.argv.slice(2)
const check = args.includes('--check')
const outIndex = args.indexOf('--out')
const outBase = outIndex === -1 ? dashboardDir : resolve(args[outIndex + 1] ?? '')
if (outIndex !== -1 && !args[outIndex + 1]) {
  console.error('gen-docs: --out needs a directory')
  process.exit(2)
}
if (check && outIndex !== -1) {
  console.error('gen-docs: --check and --out are mutually exclusive')
  process.exit(2)
}

/** Plain code-unit comparison; localeCompare would vary by host locale. */
function compareText(a, b) {
  return a < b ? -1 : a > b ? 1 : 0
}

function resolvePointer(doc, ref) {
  if (typeof ref !== 'string' || !ref.startsWith('#/')) return undefined
  return ref
    .slice(2)
    .split('/')
    .reduce((node, key) => (node == null ? undefined : node[key]), doc)
}

function deref(doc, node) {
  let current = node
  const seen = new Set()
  while (current && typeof current === 'object' && typeof current.$ref === 'string') {
    if (seen.has(current.$ref)) return undefined
    seen.add(current.$ref)
    current = resolvePointer(doc, current.$ref)
  }
  return current
}

function refName(ref) {
  return ref.slice(ref.lastIndexOf('/') + 1)
}

function schemaInfo(doc, schema) {
  const node = deref(doc, schema)
  if (!node) return { type: 'string' }
  if (node.type === 'array') {
    const item = schemaInfo(doc, node.items ?? {})
    return { type: `array<${item.type}>` }
  }
  const type = node.type ?? (node.oneOf ? 'oneOf' : node.properties ? 'object' : 'string')
  return Array.isArray(node.enum)
    ? { type, enum: node.enum.map((value) => String(value)) }
    : { type }
}

function collectParams(doc, pathItem, operation) {
  const params = []
  const positions = new Map()
  for (const raw of [...(pathItem.parameters ?? []), ...(operation.parameters ?? [])]) {
    const param = deref(doc, raw)
    if (!param?.name || !param?.in) continue
    const info = schemaInfo(doc, param.schema)
    const entry = {
      name: param.name,
      in: param.in,
      type: info.type,
      required: param.required === true || param.in === 'path',
      description: param.description ?? '',
      ...(info.enum ? { enum: info.enum } : {}),
    }
    const key = `${param.in}:${param.name}`
    const existing = positions.get(key)
    if (existing !== undefined) params[existing] = entry
    else {
      positions.set(key, params.length)
      params.push(entry)
    }
  }
  return params
}

function requestBody(doc, operation) {
  const body = deref(doc, operation.requestBody)
  if (!body?.content) return undefined
  const mediaType = Object.keys(body.content)[0]
  const media = body.content[mediaType]
  if (!media) return undefined
  const schema = media.schema
  const schemaName = schema?.$ref ? refName(schema.$ref) : schemaInfo(doc, schema).type
  const required = body.required === true
  const label = mediaType === 'application/json' ? 'JSON' : mediaType
  return {
    required,
    schema: schemaName,
    summary: `${required ? 'Required' : 'Optional'} ${label} body: ${schemaName}`,
    ...(body.description ? { description: body.description } : {}),
  }
}

function responses(doc, operation) {
  const entries = []
  for (const [status, raw] of Object.entries(operation.responses ?? {})) {
    const resolved = deref(doc, raw) ?? {}
    const description = raw?.description ?? resolved.description ?? ''
    const leading = /^([A-Z][A-Z0-9_]+):/.exec(description)?.[1]
    const code = leading && ERROR_CODES.includes(leading) ? leading : undefined
    entries.push({
      status,
      description,
      ...(code ? { code } : {}),
    })
  }
  entries.sort((a, b) => {
    const left = Number(a.status)
    const right = Number(b.status)
    const leftNumeric = Number.isFinite(left)
    const rightNumeric = Number.isFinite(right)
    if (leftNumeric && rightNumeric) return left - right
    if (leftNumeric) return -1
    if (rightNumeric) return 1
    return compareText(a.status, b.status)
  })
  return entries
}

const spec = JSON.parse(readFileSync(specPath, 'utf8'))
const { docsPages } = await import(pathToFileURL(contentPath).href)

if (!Array.isArray(docsPages) || docsPages.length === 0) {
  console.error('gen-docs: src/docs/content.ts exported no docsPages')
  process.exit(1)
}
const slugs = docsPages.map((page) => page.slug)
if (new Set(slugs).size !== slugs.length) {
  console.error(`gen-docs: duplicate content slug in ${slugs.join(', ')}`)
  process.exit(1)
}
if (slugs.includes('reference')) {
  console.error('gen-docs: "reference" is reserved for the generated reference index')
  process.exit(1)
}

const specTags = new Map((spec.tags ?? []).map((tag) => [tag.name, tag.description ?? '']))
const tagRank = new Map((spec.tags ?? []).map((tag, index) => [tag.name, index]))

const counts = {
  paths: Object.keys(spec.paths ?? {}).length,
  operations: 0,
  public: 0,
  scoped: 0,
  admin: 0,
  listed: 0,
  closed: 0,
}
const operations = []
const adminByTag = new Map()
const seenPublic = new Set()

for (const [path, pathItem] of Object.entries(spec.paths ?? {})) {
  for (const method of METHOD_ORDER) {
    const operation = pathItem?.[method]
    if (!operation) continue
    counts.operations += 1
    const specTag = operation.tags?.[0] ?? 'Other'
    const scope = operation['x-required-scope'] ?? null
    const key = `${method.toUpperCase()} ${path}`
    const access =
      PUBLIC_OPERATIONS[key] === true
        ? 'public'
        : scope && scope !== 'admin'
          ? 'scoped'
          : 'admin'
    const tag = access === 'scoped' ? (SCOPED_TAG_REMAP[specTag] ?? specTag) : specTag
    if (PUBLIC_OPERATIONS[key] === true) seenPublic.add(key)
    counts[access] += 1

    if (access === 'admin') {
      const row = adminByTag.get(tag) ?? {
        tag,
        operations: 0,
        closed: CLOSED_TAGS.includes(tag),
      }
      row.operations += 1
      adminByTag.set(tag, row)
    }

    // Closed product tags never reach the reference rows; their operations are
    // represented only by the admin count above.
    if (CLOSED_TAGS.includes(tag)) {
      counts.closed += 1
      continue
    }

    const body = requestBody(spec, operation)
    operations.push({
      tag,
      method: method.toUpperCase(),
      path,
      operationId: operation.operationId,
      summary: operation.summary ?? '',
      ...(operation.description ? { description: operation.description } : {}),
      scope,
      access,
      params: collectParams(spec, pathItem, operation),
      ...(body ? { body } : {}),
      responses: responses(spec, operation),
    })
  }
}

for (const key of Object.keys(PUBLIC_OPERATIONS)) {
  if (!seenPublic.has(key)) {
    console.error(`gen-docs: the spec no longer declares ${key}; classification is stale`)
    process.exit(1)
  }
}
if (counts.public + counts.scoped + counts.admin !== counts.operations) {
  console.error('gen-docs: classification does not add up to the operation count')
  process.exit(1)
}

operations.sort(
  (a, b) =>
    (tagRank.get(a.tag) ?? 1000) - (tagRank.get(b.tag) ?? 1000) ||
    compareText(a.tag, b.tag) ||
    compareText(a.path, b.path) ||
    METHOD_ORDER.indexOf(a.method.toLowerCase()) - METHOD_ORDER.indexOf(b.method.toLowerCase()),
)
counts.listed = operations.length

const listedTagNames = [...new Set(operations.map((operation) => operation.tag))]
listedTagNames.sort(
  (a, b) => (tagRank.get(a) ?? 1000) - (tagRank.get(b) ?? 1000) || compareText(a, b),
)
const tags = listedTagNames.map((name) => {
  const rows = operations.filter((operation) => operation.tag === name)
  return {
    name,
    description: specTags.get(name) ?? '',
    counts: {
      public: rows.filter((row) => row.access === 'public').length,
      scoped: rows.filter((row) => row.access === 'scoped').length,
      admin: rows.filter((row) => row.access === 'admin').length,
    },
  }
})

const adminTagCounts = [...adminByTag.values()].sort(
  (a, b) => (tagRank.get(a.tag) ?? 1000) - (tagRank.get(b.tag) ?? 1000) || compareText(a.tag, b.tag),
)

const reference = {
  source: 'docs/openapi.json',
  specTitle: spec.info?.title ?? '',
  specVersion: spec.info?.version ?? '',
  counts,
  tags,
  operations,
  adminTagCounts,
}

const referenceSource = `// Generated by scripts/gen-docs.mjs from docs/openapi.json. Do not edit.
// Run \`npm run docs:gen\` from dashboard/ after changing the contract.

import type { DocsReference } from './types'

export const reference: DocsReference = ${JSON.stringify(reference, null, 2)}

export default reference
`

function escapeCell(text) {
  return text.replace(/\|/g, '\\|').replace(/\r?\n/g, ' ')
}

function renderBlock(block) {
  switch (block.kind) {
    case 'p':
      return [block.text.en]
    case 'h3':
      return [`### ${block.text.en}`]
    case 'list':
      return block.items.map((item, index) =>
        block.ordered ? `${index + 1}. ${item.en}` : `- ${item.en}`,
      )
    case 'code':
      return [`**${block.label.en}**`, '', `\`\`\`${block.lang}`, block.code, '```']
    case 'table':
      return [
        `| ${block.columns.map((column) => escapeCell(column.en)).join(' | ')} |`,
        `| ${block.columns.map(() => '---').join(' | ')} |`,
        ...block.rows.map(
          (row) => `| ${row.map((cell) => escapeCell(cell.en)).join(' | ')} |`,
        ),
      ]
    case 'callout':
      return [`> **${block.title.en}** — ${block.text.en}`]
    default:
      return []
  }
}

function renderPage(page) {
  const lines = [
    `# ${page.title.en}`,
    '',
    `> ${page.summary.en}`,
    '',
    'English mirror of this page; the console page carries the Chinese translation.',
    '',
  ]
  for (const section of page.sections) {
    lines.push(`## ${section.title.en}`, '')
    for (const block of section.blocks) {
      lines.push(...renderBlock(block), '')
    }
  }
  return `${lines.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd()}\n`
}

function renderReferencePage() {
  const lines = [
    '# API reference',
    '',
    `Public and scoped operations from \`docs/openapi.json\` (${reference.specTitle} ${reference.specVersion}).`,
    '',
    `- ${counts.public} public operations`,
    `- ${counts.scoped} scoped operations`,
    `- ${counts.admin} admin-only operations, omitted from this mirror; the console shows their counts by tag only`,
    '',
    'Every path below exists in the contract. Admin-only routes are not callable with a scoped key and are not listed here.',
    '',
  ]
  for (const tag of tags) {
    const rows = operations.filter(
      (operation) => operation.tag === tag.name && operation.access !== 'admin',
    )
    if (rows.length === 0) continue
    lines.push(
      `## ${tag.name}`,
      '',
      '| Method | Path | Access | Scope | Summary |',
      '| --- | --- | --- | --- | --- |',
    )
    for (const operation of rows) {
      lines.push(
        `| ${operation.method} | \`${operation.path}\` | ${operation.access} | ${
          operation.scope ? `\`${operation.scope}\`` : '—'
        } | ${escapeCell(operation.summary)} |`,
      )
    }
    lines.push('')
  }
  return `${lines.join('\n').trimEnd()}\n`
}

function renderLlms() {
  const lines = [
    '# AIJIASU2API docs',
    '',
    '> Public documentation for the ajs-accountd control-plane API: scoped keys, catalog, leases, the loopback gate, errors, SDKs and honest limits.',
    '',
    'Links are root-relative; resolve them against the origin that serves `/docs`. Mirrors are English-only; the console page carries English and Chinese.',
    '',
    '## Start here',
    '',
    ...docsPages.map(
      (page) => `- [${page.title.en}](/docs/${page.slug}.md): ${page.summary.en}`,
    ),
    '',
    '## Reference',
    '',
    `- [API reference index](/docs/reference.md): ${counts.public} public and ${counts.scoped} scoped operations from docs/openapi.json.`,
    '',
    '## Scope',
    '',
    `This mirror covers the ${counts.public + counts.scoped} public and scoped operations. The ${counts.admin} admin-only operations are omitted; their counts appear in the console appendix only. No credential is included in any file here.`,
    '',
  ]
  return `${lines.join('\n').trimEnd()}\n`
}

const outputs = [
  { rel: 'src/docs/reference.generated.ts', content: referenceSource },
  { rel: 'public/llms.txt', content: renderLlms() },
  ...docsPages.map((page) => ({
    rel: `public/docs/${page.slug}.md`,
    content: renderPage(page),
  })),
  { rel: 'public/docs/reference.md', content: renderReferencePage() },
]

if (check) {
  const stale = outputs.filter((output) => {
    const target = join(outBase, output.rel)
    return !existsSync(target) || readFileSync(target, 'utf8') !== output.content
  })
  if (stale.length > 0) {
    console.error(`gen-docs: stale outputs: ${stale.map((output) => output.rel).join(', ')}`)
    console.error('gen-docs: run `npm run docs:gen` from dashboard/ and commit the result')
    process.exit(1)
  }
  console.log(`gen-docs: ${outputs.length} outputs up to date`)
} else {
  for (const output of outputs) {
    const target = join(outBase, output.rel)
    mkdirSync(dirname(target), { recursive: true })
    writeFileSync(target, output.content)
  }
  console.log(
    `gen-docs: wrote ${outputs.length} outputs (${counts.public} public, ${counts.scoped} scoped, ${counts.admin} admin; ${counts.listed} listed, ${counts.closed} closed omitted)`,
  )
}
