/**
 * Shared vocabulary for the public `/docs` surface.
 *
 * `reference.generated.ts` is emitted from `docs/openapi.json` by
 * `scripts/gen-docs.mjs`; `content.ts` carries the curated bilingual pages.
 * Both are rendered outside the admin shell and never import console state.
 */

/** Public docs never advertise a credential requirement they cannot satisfy. */
export type DocsAccess = 'public' | 'scoped' | 'admin'

export type DocsMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'HEAD' | 'OPTIONS'

export type DocsParamLocation = 'path' | 'query' | 'header' | 'cookie'

export interface DocsParam {
  name: string
  in: DocsParamLocation
  /** JSON-schema type, or `array<item>`; a `$ref` is resolved to its target type. */
  type: string
  required: boolean
  /** Empty when the spec carries no description. */
  description: string
  /** Allowed values when the schema is an enum. */
  enum?: string[]
}

export interface DocsBody {
  required: boolean
  /** Referenced component name, or the inline schema type. */
  schema: string
  /** One-line label for the request body, e.g. `Required JSON body: CreateLeaseInput`. */
  summary: string
  description?: string
}

export interface DocsResponse {
  /** HTTP status as written in the spec (`"200"`, `"default"`). */
  status: string
  description: string
  /** Documented stable error code when the description starts with one, e.g. `NOT_FOUND`. */
  code?: string
}

export interface DocsOperation {
  tag: string
  method: DocsMethod
  path: string
  operationId: string
  summary: string
  description?: string
  /** `x-required-scope`; `null` for routes a scoped key cannot reach. */
  scope: string | null
  access: DocsAccess
  params: DocsParam[]
  body?: DocsBody
  responses: DocsResponse[]
}

export interface DocsTagCounts {
  public: number
  scoped: number
  admin: number
}

export interface DocsTag {
  name: string
  /** Empty for a tag the spec never declares at the document level. */
  description: string
  counts: DocsTagCounts
}

/**
 * Admin-only appendix: counts and tag names, never callable rows.
 * Closed product tags (`Portal`, `Order Admin`, `Tenant Admin`, `Payments`)
 * appear here and nowhere else in the generated reference.
 */
export interface DocsAdminTagCount {
  tag: string
  operations: number
  closed: boolean
}

export interface DocsCounts {
  /** Paths in the spec, including every admin-only path. */
  paths: number
  /** Operations in the spec, including every admin-only operation. */
  operations: number
  public: number
  scoped: number
  admin: number
  /** Operations emitted in `reference.operations` (spec total minus closed tags). */
  listed: number
  /** Operations omitted because their tag is closed. */
  closed: number
}

export interface DocsReference {
  source: string
  specTitle: string
  specVersion: string
  counts: DocsCounts
  /** Tags that have at least one listed operation, in spec order. */
  tags: DocsTag[]
  /** Sorted by tag order, then path, then method; closed tags are absent. */
  operations: DocsOperation[]
  /** Every admin operation grouped by tag, including closed tags. */
  adminTagCounts: DocsAdminTagCount[]
}

/** Every curated string exists in both site languages. */
export interface DocsText {
  en: string
  zh: string
}

export type DocsCodeLanguage = 'bash' | 'json' | 'go' | 'ts' | 'http'

/**
 * A block inside a curated page. `DocsText` values may carry inline Markdown
 * (code spans and links); block structure stays explicit so the app and the
 * Markdown mirror render the same content.
 */
export type DocsBlock =
  | { kind: 'p'; text: DocsText }
  | { kind: 'h3'; text: DocsText }
  | { kind: 'list'; ordered?: boolean; items: DocsText[] }
  | { kind: 'code'; lang: DocsCodeLanguage; label: DocsText; code: string }
  | { kind: 'table'; columns: DocsText[]; rows: DocsText[][] }
  | {
      kind: 'callout'
      tone: 'info' | 'warn' | 'pending' | 'admin'
      title: DocsText
      text: DocsText
    }

export interface DocsSection {
  /** Stable anchor, unique within the page. */
  id: string
  title: DocsText
  blocks: DocsBlock[]
}

export interface DocsPage {
  /** Route segment: `/docs/<slug>`; `overview` is served at `/docs`. */
  slug: string
  title: DocsText
  summary: DocsText
  sections: DocsSection[]
}
