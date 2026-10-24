import { Power, RefreshCw, Server } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../lib/api'
import { useI18n } from '../lib/i18n'
import { DEFAULT_CATALOG_FILTERS } from '../lib/nodes'
import type { CatalogFilters } from '../lib/nodes'
import type { AccountServers, CatalogSnapshot } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { Button, PageHeader, SegmentedControl, Select } from '../components/primitives'
import { CatalogTab } from '../components/nodes/CatalogTab'
import { ConnectedTab } from '../components/nodes/ConnectedTab'

type NodesTab = 'catalog' | 'connected'

const EMPTY_SNAPSHOT: CatalogSnapshot = {
  enabled: true,
  accountId: '',
  fetchedAt: null,
  stale: false,
  total: 0,
  servers: [],
}

const EMPTY_CATALOG: AccountServers = {
  enabled: true,
  accountId: '',
  fetchedAt: null,
  stale: false,
  total: 0,
  offset: 0,
  limit: 50,
  nextOffset: 0,
  hasMore: false,
  regions: [],
  servers: [],
}

/**
 * Seeds the search box from the `q` param and follows later `q` changes (the command palette
 * can navigate here while the page is mounted) without clobbering a query the user typed on
 * re-renders that leave `q` alone.
 */
function useParamQuery(paramValue: string | null): [string, (value: string) => void] {
  const [query, setQuery] = useState(paramValue ?? '')
  const lastParamRef = useRef(paramValue)
  useEffect(() => {
    if (paramValue !== lastParamRef.current) {
      lastParamRef.current = paramValue
      setQuery(paramValue ?? '')
    }
  }, [paramValue])
  return [query, setQuery]
}

/** Keeps a server-side filter from firing a request on every keystroke. */
function useDebouncedValue<T>(value: T, delay = 250): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay)
    return () => window.clearTimeout(timer)
  }, [value, delay])
  return debounced
}

/**
 * Two views of the same vendor account set: the catalog the daemon retained
 * from the last login (what could be served) and the nodes it actually runs.
 * The catalog is read one account at a time because the daemon pages the
 * per-account endpoint; the account picker sits in the header on that tab.
 */
export default function Nodes() {
  const { t } = useI18n()
  const [params] = useSearchParams()
  const [tab, setTab] = useState<NodesTab>('catalog')
  const [query, setQuery] = useParamQuery(params.get('q'))
  const [selectedServers, setSelectedServers] = useState<string[]>([])
  const [catalogAccountId, setCatalogAccountId] = useState('')
  const [catalogFilters, setCatalogFilters] = useState<CatalogFilters>(DEFAULT_CATALOG_FILTERS)

  const nodes = useAsync(() => api.listNodes(), [], { intervalMs: 15_000 })
  const accounts = useAsync(() => api.listAccounts(), [])
  const queryNeedle = query.trim().toLowerCase()
  const queryNode = queryNeedle
    ? nodes.data?.find((node) =>
        [node.label, node.accountLabel, node.region, node.city, node.egressIp]
          .join(' ')
          .toLowerCase()
          .includes(queryNeedle),
      )
    : undefined
  const selectedAccount =
    accounts.data?.find((account) => account.id === catalogAccountId) ??
    (queryNode
      ? accounts.data?.find((account) => account.id === queryNode.accountId)
      : undefined) ??
    accounts.data?.[0]
  const catalogAccount = selectedAccount?.id ?? ''

  // A palette node search should open the catalog of the account that owns the
  // node; the manual account picker still wins once the operator chooses one.
  useEffect(() => {
    if (!catalogAccountId && queryNode) setCatalogAccountId(queryNode.accountId)
  }, [catalogAccountId, queryNode])

  const catalogQuery = useDebouncedValue(query)

  const catalog = useAsync(
    () =>
      catalogAccount
        ? api.getAccountServers(catalogAccount, {
            q: catalogQuery.trim() || undefined,
            region: catalogFilters.region === 'all' ? undefined : catalogFilters.region,
            status: catalogFilters.status === 'all' ? undefined : catalogFilters.status,
            sort: catalogFilters.sort,
            offset: catalogFilters.offset,
            limit: catalogFilters.limit,
          })
        : Promise.resolve(EMPTY_CATALOG),
    [
      catalogAccount,
      catalogQuery,
      catalogFilters.region,
      catalogFilters.status,
      catalogFilters.sort,
      catalogFilters.offset,
      catalogFilters.limit,
    ],
    { intervalMs: 30_000 },
  )

  // The whole retained catalog for the map and the province filter. Wait for
  // this account's catalog response before reading it: the first pass then
  // carries the daemon's real snapshot timestamp, so the follow-up effect
  // cannot start a second six-page pass. A failed catalog still gets one
  // attempt so the map can show its own error.
  const catalogSettled =
    catalog.data?.accountId === catalogAccount ||
    catalog.status === 'error' ||
    (catalog.error !== undefined && catalog.data?.accountId !== catalogAccount)
  const snapshot = useAsync(
    () =>
      catalogAccount && catalogSettled
        ? api.getCatalogSnapshot(catalogAccount, catalog.data?.fetchedAt)
        : Promise.resolve(EMPTY_SNAPSHOT),
    [catalogAccount, catalogSettled, catalog.data?.fetchedAt],
  )

  const changeQuery = (value: string) => {
    setQuery(value)
    setCatalogFilters((current) => ({ ...current, offset: 0 }))
  }

  const changeCatalogAccount = (value: string) => {
    setCatalogAccountId(value)
    // The geo filter belongs to the previous account's geography.
    setCatalogFilters((current) => ({ ...current, offset: 0, province: '', city: '' }))
  }

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('nodes.title')}
        subtitle={t('nodes.subtitle')}
        refresh={summarizeRefresh([nodes, accounts, catalog, snapshot])}
        actions={
          <>
            {tab === 'catalog' ? (
              <Select
                variant="form"
                size="sm"
                searchable
                aria-label={t('nodes.catalog.accountAria')}
                value={catalogAccount}
                onChange={changeCatalogAccount}
                disabled={(accounts.data ?? []).length === 0}
                options={(accounts.data ?? []).map((account) => ({
                  value: account.id,
                  label: account.label,
                }))}
              />
            ) : null}
            <Button
              variant="secondary"
              icon={<RefreshCw size={14} />}
              onClick={() => {
                nodes.reload()
                accounts.reload()
                catalog.reload()
                if (catalogAccount) api.invalidateCatalogSnapshot(catalogAccount)
                snapshot.reload()
              }}
            >
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <SegmentedControl
        label={t('nodes.viewsAria')}
        value={tab}
        onChange={setTab}
        options={[
          { value: 'catalog', label: t('nodes.tab.catalog'), icon: <Server size={13} /> },
          { value: 'connected', label: t('nodes.tab.connected'), icon: <Power size={13} /> },
        ]}
      />

      {tab === 'catalog' ? (
        <CatalogTab
          catalog={catalog}
          accountId={catalogAccount}
          accountLabel={selectedAccount?.label ?? ''}
          accounts={accounts.data ?? []}
          accountsLoading={accounts.status === 'loading' && !accounts.data}
          filters={catalogFilters}
          onFiltersChange={setCatalogFilters}
          query={query}
          activeQuery={catalogQuery}
          onQueryChange={changeQuery}
          snapshot={snapshot}
          selected={selectedServers}
          onSelectedChange={setSelectedServers}
        />
      ) : (
        <ConnectedTab nodes={nodes} query={query} onQueryChange={setQuery} />
      )}
    </div>
  )
}
