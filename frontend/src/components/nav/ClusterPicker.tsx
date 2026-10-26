import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Check, ChevronDown, Search } from 'lucide-react'
import * as clustersApi from '../../api/clusters'
import { Popover, PopoverContent, PopoverTrigger, StatusDot } from '../ui'
import { clusterConnection, connectionLabelKey, connectionTone } from '../../lib/status'

// Above this number of clusters the search box appears inside the dropdown.
// Only when the list does not fit at a glance.
const SEARCH_THRESHOLD = 8

interface ClusterPickerProps {
  /** Active cluster according to the route, if we are inside one. */
  activeClusterId?: number
  /** Current tab, so switching cluster does not go back to Pods. */
  activeTab?: string
}

/**
 * Cluster picker in the topbar. Until now switching cluster forced going back
 * to the Dashboard, because the only way into a cluster was its card.
 */
export function ClusterPicker({ activeClusterId, activeTab }: ClusterPickerProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState('')

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: clustersApi.listClusters })

  const clusters = clustersQuery.data ?? []
  const active = clusters.find((c) => c.id === activeClusterId)

  if (clusters.length === 0) return null

  const query = filter.trim().toLowerCase()
  const visible = query ? clusters.filter((c) => c.displayName.toLowerCase().includes(query)) : clusters

  function select(id: number) {
    setOpen(false)
    setFilter('')
    // The tab is kept when switching cluster, but NOT the namespace or the
    // search: another cluster's namespaces are different ones, and dragging
    // along an `ns` that does not exist there leaves the table empty for no
    // apparent reason. Without an active tab we are on the overview, and that
    // is where to land.
    navigate(activeTab ? `/clusters/${id}/${activeTab}` : `/clusters/${id}`)
  }

  return (
    <Popover open={open} onOpenChange={(o) => { setOpen(o); if (!o) setFilter('') }}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="flex min-w-0 max-w-32 items-center gap-2 rounded-md border border-topbar-border px-2.5 py-1.5 text-sm text-topbar-text hover:bg-topbar-surface md:max-w-56"
        >
          <span className="truncate">{active?.displayName ?? t('nav.selectCluster')}</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 opacity-70" aria-hidden="true" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-72">
        {clusters.length > SEARCH_THRESHOLD && (
          <div className="flex items-center gap-2 border-b border-border px-3 py-2">
            <Search className="h-3.5 w-3.5 shrink-0 text-fg-muted" aria-hidden="true" />
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t('nav.filterClusters')}
              autoFocus
              className="w-full bg-transparent text-sm"
            />
          </div>
        )}
        <ul className="max-h-72 overflow-y-auto py-1">
          {visible.map((c) => (
            <li key={c.id}>
              <button
                type="button"
                onClick={() => select(c.id)}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-surface-hover"
              >
                <StatusDot
                  tone={connectionTone(clusterConnection(c))}
                  title={t(connectionLabelKey(clusterConnection(c)))}
                />
                <span className="min-w-0 flex-1 truncate font-mono">{c.displayName}</span>
                {c.id === activeClusterId && (
                  <Check className="h-3.5 w-3.5 shrink-0 text-primary" aria-hidden="true" />
                )}
              </button>
            </li>
          ))}
          {visible.length === 0 && (
            <li className="px-3 py-2 text-sm text-fg-muted">{t('common.noResults')}</li>
          )}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
