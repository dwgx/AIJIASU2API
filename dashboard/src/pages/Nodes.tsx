import { Power, RefreshCw, Server } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../lib/api'
import { useI18n } from '../lib/i18n'
import { loadServerCatalog } from '../lib/nodes'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { Button, PageHeader, SegmentedControl } from '../components/primitives'
import { CatalogTab } from '../components/nodes/CatalogTab'
import { ConnectedTab } from '../components/nodes/ConnectedTab'

type NodesTab = 'catalog' | 'connected'

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

/**
 * Two views of the same vendor account set: the catalog the daemon retained
 * from the last login (what could be served) and the nodes it actually runs.
 */
export default function Nodes() {
  const { t } = useI18n()
  const [params] = useSearchParams()
  const [tab, setTab] = useState<NodesTab>('catalog')
  const [query, setQuery] = useParamQuery(params.get('q'))
  const [selectedServers, setSelectedServers] = useState<string[]>([])
  const nodes = useAsync(() => api.listNodes(), [], { intervalMs: 15_000 })
  const catalog = useAsync(() => loadServerCatalog(), [], { intervalMs: 30_000 })

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('nodes.title')}
        subtitle={t('nodes.subtitle')}
        refresh={summarizeRefresh([nodes, catalog])}
        actions={
          <Button
            variant="secondary"
            icon={<RefreshCw size={14} />}
            onClick={() => {
              nodes.reload()
              catalog.reload()
            }}
          >
            {t('common.refresh')}
          </Button>
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
          query={query}
          onQueryChange={setQuery}
          selected={selectedServers}
          onSelectedChange={setSelectedServers}
        />
      ) : (
        <ConnectedTab nodes={nodes} query={query} onQueryChange={setQuery} />
      )}
    </div>
  )
}
