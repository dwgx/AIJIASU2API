import { provinceCodeForTitle, resolveServerCity } from './geo'
import type { RegionCount, ServerCatalogSort, ServerView } from './types'

/**
 * One vendor server joined with the account whose login produced it. The
 * daemon answers per account, so a catalog view is scoped to one account and
 * every row still carries its account label.
 */
export interface CatalogRow extends ServerView {
  accountId: string
  accountLabel: string
  /** The snapshot is retained but the daemon could not refresh it. */
  stale: boolean
}

/** Server ids are only unique per account, so selection keys are qualified. */
export function catalogRowKey(row: CatalogRow): string {
  return `${row.accountId}:${row.id}`
}

/**
 * Server-side catalog query state. `region`/`status` use 'all' for "no
 * filter"; offset and limit are sent to the daemon, so the page never holds
 * more than one window of the catalog.
 */
export interface CatalogFilters {
  region: string
  status: string
  sort: ServerCatalogSort
  offset: number
  limit: number
  /** GB/T 2260 two-digit province code, `00` for the Other bucket, '' for all. */
  province: string
  /** City code from the bundled geo asset, '' for all cities in the province. */
  city: string
}

export const DEFAULT_CATALOG_FILTERS: CatalogFilters = {
  region: 'all',
  status: 'all',
  sort: 'order',
  offset: 0,
  limit: 50,
  province: '',
  city: '',
}

/** Stable region order for the grouped catalog; rows keep their input order. */
export function groupCatalogByRegion(
  rows: CatalogRow[],
): Array<{ region: string; rows: CatalogRow[] }> {
  const groups = new Map<string, CatalogRow[]>()
  for (const row of rows) {
    const bucket = groups.get(row.region)
    if (bucket) bucket.push(row)
    else groups.set(row.region, [row])
  }
  return Array.from(groups, ([region, groupRows]) => ({ region, rows: groupRows })).sort((a, b) =>
    a.region.localeCompare(b.region),
  )
}

/** Counts for the group headers when rows are filtered in the browser. */
export function summarizeCatalogRegions(rows: readonly CatalogRow[]): RegionCount[] {
  const counts = new Map<string, number>()
  for (const row of rows) counts.set(row.region, (counts.get(row.region) ?? 0) + 1)
  return [...counts.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([code, count]) => ({ code, count }))
}

export interface CatalogRowFilter {
  q?: string
  status?: string
  region?: string
  province?: string
  city?: string
}

/**
 * Client-side counterpart of the daemon's q/region/status filter, plus the
 * province/city filters the backend does not have yet. The search fields are
 * exactly the daemon's (id, title, host, region) so a filtered page and a
 * snapshot-derived page agree. `province: '00'` selects the unresolved bucket.
 */
export function filterCatalogRows(
  rows: readonly CatalogRow[],
  filter: CatalogRowFilter,
): CatalogRow[] {
  const needle = (filter.q ?? '').trim().toLowerCase()
  const status = filter.status && filter.status !== 'all' ? filter.status.toLowerCase() : ''
  const region = filter.region && filter.region !== 'all' ? filter.region.toLowerCase() : ''
  const province = filter.province ?? ''
  const city = filter.city ?? ''
  if (!needle && !status && !region && !province && !city) return [...rows]
  return rows.filter((row) => {
    if (
      needle &&
      ![row.id, row.title, row.host, row.region].join(' ').toLowerCase().includes(needle)
    ) {
      return false
    }
    if (status && row.status.toLowerCase() !== status) return false
    if (region && row.region.toLowerCase() !== region) return false
    if (province && provinceCodeForTitle(row.title) !== province) return false
    if (city) {
      const resolved = resolveServerCity(row.title)
      if (!resolved || resolved.code !== city) return false
    }
    return true
  })
}

/**
 * Sorts a browser-held page with the same keys the daemon accepts. The server
 * tie-breaks equal keys by id; doing the same keeps a filtered view stable
 * across polls.
 */
export function sortCatalogRows(
  rows: readonly CatalogRow[],
  sort: ServerCatalogSort,
): CatalogRow[] {
  if (sort === 'order') return [...rows]
  const compare = (left: CatalogRow, right: CatalogRow): number => {
    switch (sort) {
      case 'title':
        return left.title.toLowerCase().localeCompare(right.title.toLowerCase())
      case 'host':
        return left.host.toLowerCase().localeCompare(right.host.toLowerCase())
      case 'status':
        return left.status.toLowerCase().localeCompare(right.status.toLowerCase())
      case 'region':
        return left.region.toLowerCase().localeCompare(right.region.toLowerCase())
      default:
        return 0
    }
  }
  return [...rows].sort((left, right) => compare(left, right) || left.id.localeCompare(right.id))
}
