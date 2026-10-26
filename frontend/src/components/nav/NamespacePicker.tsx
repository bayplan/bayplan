import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Check, ChevronDown, Search } from 'lucide-react'
import * as resources from '../../api/resources'
import { useUrlState } from '../../hooks/useUrlState'
import { usePreferences } from '../../hooks/usePreferences'
import { ageParts } from '../../lib/status'
import { joinNamespaces, splitNamespaces } from '../../lib/namespaces'
import { Popover, PopoverContent, PopoverTrigger } from '../ui'

/**
 * Namespace filter, in the topbar and not inside the page: it is cluster
 * context and applies to all its listings.
 *
 * Multi-select WITHOUT classifying anything. Offering "user namespaces
 * only" / "system only" was discarded because there is no reliable
 * criterion: in a real cluster we looked at, `kasten-io` was created by the team and
 * `node-feature-discovery` by the platform, and neither the `-system` suffix
 * nor the labels tell them apart. And the failure is asymmetric: if a heuristic
 * hides a team namespace, the table says "0 pods" and the user believes it.
 *
 * Instead, the AGE of each namespace is shown and the newest are sorted
 * first: the ones the cluster deployment creates are all born in the same
 * minute, so the team's come out on top by themselves. And the selection is
 * remembered per cluster, so it is chosen once.
 */
export function NamespacePicker({ clusterId }: { clusterId: number }) {
  const { t } = useTranslation()
  const [filters, setFilters] = useUrlState(['ns'] as const)
  const { preferences, update } = usePreferences()
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState('')

  const namespacesQuery = useQuery({
    queryKey: ['namespaces', clusterId],
    queryFn: () => resources.listNamespaces(clusterId),
  })
  // Memoized because they feed effects and memos: `?? []` creates a new array
  // on every render and would drag recalculations (and dependency warnings)
  // for nothing.
  const all = useMemo(() => namespacesQuery.data ?? [], [namespacesQuery.data])
  const selected = splitNamespaces(filters.ns)
  const saved = useMemo(
    () => preferences.namespacesByCluster[String(clusterId)] ?? [],
    [preferences.namespacesByCluster, clusterId],
  )

  // Remembered value: if the cluster is entered WITHOUT `ns` in the URL and
  // there is a saved selection, it is applied and written to the URL.
  //
  // The filter does not live only in the preferences: then a shared link would
  // not carry it and the recipient would see "that resource does not show up
  // for me". Here the preference
  // only decides the INITIAL value: the URL rules, so the link you copy carries
  // what you are seeing.
  const applied = useRef<number | null>(null)
  useEffect(() => {
    if (applied.current === clusterId) return
    // Marked as applied ONLY when there is something to apply. On a full page
    // load this effect runs before /api/auth/me responds, so the preferences
    // are still empty: if it were marked here, once they arrived it would not
    // be retried and the saved selection would never be applied.
    if (saved.length === 0) return
    applied.current = clusterId
    if (filters.ns === '') {
      setFilters({ ns: joinNamespaces(saved) })
    }
  }, [clusterId, filters.ns, saved, setFilters])

  // Newest first: that is what puts the team's namespaces on top without
  // having to decide which ones they are.
  const ordered = useMemo(
    () => [...all].sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()),
    [all],
  )
  const query = filter.trim().toLowerCase()
  const visible = query ? ordered.filter((n) => n.name.toLowerCase().includes(query)) : ordered

  /** Saves the selection to the URL and as a preference for THIS cluster. */
  function apply(namespaces: string[]) {
    setFilters({ ns: joinNamespaces(namespaces) })
    void update({
      namespacesByCluster: { ...preferences.namespacesByCluster, [String(clusterId)]: namespaces },
    })
  }

  function toggle(name: string) {
    apply(selected.includes(name) ? selected.filter((n) => n !== name) : [...selected, name])
  }

  const label =
    selected.length === 0
      ? t('cluster.allNamespaces')
      : selected.length === 1
        ? selected[0]
        : t('cluster.namespaceSelected', { count: selected.length, total: all.length })

  return (
    <Popover open={open} onOpenChange={(o) => { setOpen(o); if (!o) setFilter('') }}>
      <PopoverTrigger asChild>
        <button
          type="button"
          title={selected.length > 1 ? selected.join(', ') : undefined}
          className="flex min-w-0 max-w-28 items-center gap-2 rounded-md border border-topbar-border px-2.5 py-1.5 text-sm text-topbar-text hover:bg-topbar-surface md:max-w-48"
        >
          <span className="truncate">{label}</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 opacity-70" aria-hidden="true" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-80">
        <div>
          <div className="flex items-center gap-2 border-b border-border px-3 py-2">
            <Search className="h-3.5 w-3.5 shrink-0 text-fg-muted" aria-hidden="true" />
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t('cluster.searchNamespace')}
              autoFocus
              className="w-full bg-transparent text-sm "
            />
          </div>

          <div className="border-b border-border py-1">
            <button
              type="button"
              onClick={() => apply([])}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-surface-hover"
            >
              <span className="w-4">{selected.length === 0 && <Check className="h-3.5 w-3.5" aria-hidden="true" />}</span>
              {t('cluster.allNamespaces')}
            </button>
            {/* Shortcut back to the saved selection without rebuilding it by hand
                after having gone through "all". */}
            {saved.length > 0 && joinNamespaces(saved) !== filters.ns && (
              <button
                type="button"
                onClick={() => setFilters({ ns: joinNamespaces(saved) })}
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-surface-hover"
              >
                <span className="w-4" />
                {t('cluster.mySelection', { count: saved.length })}
              </button>
            )}
          </div>

          <ul className="max-h-72 overflow-y-auto py-1">
            {visible.map((n) => {
              const parts = ageParts(n.createdAt)
              return (
                <li key={n.name}>
                  <button
                    type="button"
                    onClick={() => toggle(n.name)}
                    className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-surface-hover"
                  >
                    <span className="w-4 shrink-0">
                      {selected.includes(n.name) && <Check className="h-3.5 w-3.5 text-primary" aria-hidden="true" />}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono">{n.name}</span>
                    <span className="shrink-0 text-xs text-fg-muted">
                      {parts ? t(`common.age.${parts.unit}`, { value: parts.value }) : ''}
                    </span>
                  </button>
                </li>
              )
            })}
            {visible.length === 0 && (
              <li className="px-3 py-2 text-sm text-fg-muted">{t('common.noResults')}</li>
            )}
          </ul>

          <p className="border-t border-border px-3 py-2 text-xs text-fg-muted">
            {t('cluster.namespaceAgeHint')}
          </p>
        </div>
      </PopoverContent>
    </Popover>
  )
}
