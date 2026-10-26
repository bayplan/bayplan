import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { Search } from 'lucide-react'
import type { Cluster } from '../../api/clusters'
import type {
  DeploymentSummary,
  NamespaceSummary,
  NodeSummary,
  PodSummary,
  ServiceSummary,
} from '../../api/resources'
import { Modal } from '../ui'
import { useAuth } from '../../hooks/useAuth'

// Cap on results per group. Without it, typing "a" in a cluster with hundreds
// of pods fills the palette and it stops serving its purpose: picking fast. When
// it is cut, it says how many are left out, instead of pretending there are no more.
const MAX_PER_GROUP = 6

interface Item {
  key: string
  label: string
  hint?: string
  to: string
}

interface CommandPaletteProps {
  onClose: () => void
  activeClusterId?: number
  isGlobalAdmin: boolean
}

/**
 * Global search with Ctrl/Cmd+K. It only reads from the TanStack Query cache: it
 * fires no new request, so what it finds is exactly what the application
 * already had loaded.
 *
 * It searches instances, not only resource **types**: clusters, namespaces and
 * the active cluster's resources.
 * Searching real instances, across all clusters and without depending on what
 * is in cache, would need a search endpoint in the backend.
 */
export function CommandPalette({ onClose, activeClusterId, isGlobalAdmin }: CommandPaletteProps) {
  const { t } = useTranslation()
  const { user } = useAuth()
  // SSO users have no local password: the page would only fail.
  const isLocal = user?.source === 'local'
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [query, setQuery] = useState('')
  const [highlighted, setHighlighted] = useState(0)

  const term = query.trim().toLowerCase()

  const groups = useMemo(() => {
    const matches = (text: string) => text.toLowerCase().includes(term)

    const navItems: Item[] = [
      { key: 'nav-dashboard', label: t('nav.dashboard'), to: '/' },
      ...(isLocal ? [{ key: 'nav-password', label: t('changePassword.title'), to: '/change-password' }] : []),
      ...(isGlobalAdmin
        ? [
            { key: 'nav-clusters', label: t('nav.clusters'), to: '/admin/clusters' },
            { key: 'nav-users', label: t('nav.users'), to: '/admin/users' },
          ]
        : []),
    ].filter((i) => matches(i.label))

    const clusters = (queryClient.getQueryData<Cluster[]>(['clusters']) ?? [])
      .filter((c) => matches(c.displayName))
      .map((c) => ({
        key: `cluster-${c.id}`,
        label: c.displayName,
        hint: c.apiEndpoint,
        to: `/clusters/${c.id}/pods`,
      }))

    // Namespaces and resources only apply inside a cluster: outside one there
    // is no context to take the user to.
    const namespaces: Item[] =
      activeClusterId === undefined
        ? []
        : (queryClient.getQueryData<NamespaceSummary[]>(['namespaces', activeClusterId]) ?? [])
            .filter((ns) => matches(ns.name))
            .map(({ name: ns }) => ({
              key: `ns-${ns}`,
              label: ns,
              hint: t('cluster.namespaceLabel'),
              to: `/clusters/${activeClusterId}/pods?ns=${encodeURIComponent(ns)}`,
            }))

    // An empty term lists no resources: it would be hundreds of rows nobody
    // asked for.
    const resources: Item[] = []
    if (activeClusterId !== undefined && term !== '') {
      // Deduplication is mandatory: getQueriesData with the partial key returns
      // ALL the namespace variants in cache, and the same pod is at once in the
      // "all namespaces" entry and in its own namespace's, so without this it
      // shows up repeated as many times as listings have been visited.
      const seen = new Set<string>()
      const collect = <T extends { name: string; namespace?: string }>(kind: string, tab: string) => {
        const cached = queryClient.getQueriesData<T[]>({ queryKey: [kind, activeClusterId] })
        for (const [, rows] of cached) {
          for (const row of rows ?? []) {
            if (!matches(row.name)) continue
            const key = `${kind}-${row.namespace ?? ''}-${row.name}`
            if (seen.has(key)) continue
            seen.add(key)
            const params = new URLSearchParams()
            if (row.namespace) params.set('ns', row.namespace)
            params.set('q', row.name)
            resources.push({
              key,
              label: row.name,
              hint: [t(`cluster.tabs.${tab}`), row.namespace].filter(Boolean).join(' · '),
              to: `/clusters/${activeClusterId}/${tab}?${params.toString()}`,
            })
          }
        }
      }
      collect<PodSummary>('pods', 'pods')
      collect<DeploymentSummary>('deployments', 'deployments')
      collect<ServiceSummary>('services', 'services')
      collect<NodeSummary>('nodes', 'nodes')
    }

    return [
      { label: t('palette.groupNav'), items: navItems },
      { label: t('palette.groupClusters'), items: clusters },
      { label: t('palette.groupNamespaces'), items: namespaces },
      { label: t('palette.groupResources'), items: resources },
    ].filter((g) => g.items.length > 0)
  }, [term, activeClusterId, isGlobalAdmin, isLocal, queryClient, t])

  // Flat list in the same order it is painted, so arrows and Enter match what
  // is seen (each group cut to MAX_PER_GROUP).
  const flat = groups.flatMap((g) => g.items.slice(0, MAX_PER_GROUP))
  const active = flat[Math.min(highlighted, flat.length - 1)]

  function go(item: Item) {
    onClose()
    navigate(item.to)
  }

  return (
    <Modal onClose={onClose} variant="top" className="overflow-hidden">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
        <Search className="h-4 w-4 shrink-0 text-fg-muted" aria-hidden="true" />
        <input
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            setHighlighted(0)
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setHighlighted((h) => Math.min(h + 1, flat.length - 1))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setHighlighted((h) => Math.max(h - 1, 0))
            } else if (e.key === 'Enter' && active) {
              e.preventDefault()
              go(active)
            }
          }}
          placeholder={t('palette.placeholder')}
          aria-label={t('palette.placeholder')}
          className="w-full bg-transparent text-sm placeholder:text-fg-muted"
        />
      </div>

      <div className="max-h-80 overflow-y-auto py-1">
        {flat.length === 0 && (
          <p className="px-3 py-6 text-center text-sm text-fg-muted">{t('palette.empty')}</p>
        )}
        {groups.map((group) => {
          const shown = group.items.slice(0, MAX_PER_GROUP)
          const hidden = group.items.length - shown.length
          return (
            <div key={group.label}>
              <p className="px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg-muted uppercase">
                {group.label}
              </p>
              {shown.map((item) => (
                <button
                  key={item.key}
                  type="button"
                  // onMouseMove and not onMouseEnter: when the palette opens under
                  // the cursor, enter would fire without anyone moving the mouse.
                  onMouseMove={() => setHighlighted(flat.indexOf(item))}
                  onClick={() => go(item)}
                  className={`flex w-full items-baseline gap-2 px-3 py-2 text-left text-sm ${
                    item === active ? 'bg-primary-soft' : ''
                  }`}
                >
                  <span className="min-w-0 flex-1 truncate font-mono">{item.label}</span>
                  {item.hint && (
                    <span className="shrink-0 text-xs text-fg-muted">{item.hint}</span>
                  )}
                </button>
              ))}
              {hidden > 0 && (
                <p className="px-3 pb-1 text-xs text-fg-muted">
                  {t('palette.more', { count: hidden })}
                </p>
              )}
            </div>
          )
        })}
      </div>
    </Modal>
  )
}
