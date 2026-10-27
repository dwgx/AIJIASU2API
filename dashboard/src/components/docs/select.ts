import { reference } from '../../docs/reference.generated'
import type { DocsOperation, DocsTag } from '../../docs/types'

/**
 * The public reference is the generated list minus every operator-only row.
 * 107 generated operations carry 56 customer-callable ones (2 public health
 * checks + 54 scoped operations); the other 51 admin rows stay out of the
 * callable surface and are represented only by the admin appendix counts.
 */
export const customerOperations: DocsOperation[] = reference.operations.filter(
  (operation) => operation.access !== 'admin',
)

/** Tags with at least one customer-callable operation, in spec order. */
export const customerTags: DocsTag[] = reference.tags.filter((tag) =>
  customerOperations.some((operation) => operation.tag === tag.name),
)

/** Route-safe tag segment: `Account IO` → `account-io`. */
export function tagSlug(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/** In-page anchor for a tag group, kept distinct from operation ids. */
export function tagAnchor(name: string): string {
  return `tag-${tagSlug(name)}`
}

/** Decode a route segment without letting a malformed escape crash the page. */
export function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}
