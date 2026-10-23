import { api } from './api'
import type { ServerView } from './types'

/**
 * One vendor server joined with the account whose login produced it. The
 * daemon answers per account, so the page merges the catalogs and every row
 * has to carry its account label.
 */
export interface CatalogRow extends ServerView {
  accountId: string
  accountLabel: string
  /** The snapshot is retained but the daemon could not refresh it. */
  stale: boolean
}

export interface ServerCatalog {
  rows: CatalogRow[]
  /** Accounts the catalog was requested for. */
  accounts: number
  /** Accounts whose snapshot is retained but not refreshed. */
  staleAccounts: string[]
  /** Accounts whose request failed; their rows are missing, not empty. */
  failedAccounts: string[]
  /** First `{enabled:false}` reason; account login is off when set. */
  disabledReason: string | null
  /** Latest `fetchedAt` across accounts, null when nothing was retained. */
  fetchedAt: string | null
}

/** Server ids are only unique per account, so selection keys are qualified. */
export function catalogRowKey(row: CatalogRow): string {
  return `${row.accountId}:${row.id}`
}

/**
 * Reads every account's vendor catalog. A failing account does not blank the
 * page: its label is reported and the other catalogs still render.
 */
export async function loadServerCatalog(): Promise<ServerCatalog> {
  const accounts = await api.listAccounts()
  const results = await Promise.all(
    accounts.map(async (account) => {
      try {
        return { account, catalog: await api.getAccountServers(account.id) }
      } catch (cause) {
        return { account, error: cause instanceof Error ? cause.message : String(cause) }
      }
    }),
  )

  const rows: CatalogRow[] = []
  const staleAccounts: string[] = []
  const failedAccounts: string[] = []
  let disabledReason: string | null = null
  let fetchedAt: string | null = null

  for (const result of results) {
    if ('error' in result) {
      failedAccounts.push(result.account.label)
      continue
    }
    const { account, catalog } = result
    if (catalog.enabled === false && disabledReason === null) {
      disabledReason = catalog.reason ?? ''
    }
    if (catalog.stale) staleAccounts.push(account.label)
    if (catalog.fetchedAt && (fetchedAt === null || catalog.fetchedAt > fetchedAt)) {
      fetchedAt = catalog.fetchedAt
    }
    for (const server of catalog.servers) {
      rows.push({ ...server, accountId: account.id, accountLabel: account.label, stale: catalog.stale })
    }
  }

  rows.sort(
    (a, b) =>
      a.region.localeCompare(b.region) ||
      a.title.localeCompare(b.title) ||
      a.id.localeCompare(b.id),
  )

  return {
    rows,
    accounts: accounts.length,
    staleAccounts,
    failedAccounts,
    disabledReason,
    fetchedAt,
  }
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
