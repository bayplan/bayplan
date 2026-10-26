import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { ExternalLink, Package, RefreshCw, X } from 'lucide-react'
import * as helmApi from '../api/helm'
import { listClusters } from '../api/clusters'
import { SearchInput } from '../components/SearchInput'
import { ReleaseEditorModal, type ReleaseEditorPreset } from '../components/helm/ReleaseEditorModal'
import { Alert, Badge, Button, Card, EmptyState, Input, PageHeader, Select, Toolbar } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { useAuth } from '../hooks/useAuth'
import { apiErrorText } from '../lib/errors'
import { NotFound } from './NotFound'
import { safeHttpUrl } from '../lib/url'

/**
 * Chart catalog: pick a repo, search, install. The repos are managed by the
 * global admin in /admin/helm-repos; here they are only consumed.
 */
export function HelmCatalog() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { user } = useAuth()
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const canWrite = cluster?.role === 'admin' || cluster?.role === 'editor'

  const [filters, setFilters] = useUrlState(['ns', 'q', 'repo'] as const)
  const namespaces = splitNamespaces(filters.ns)
  const reposQuery = useQuery({ queryKey: ['helm-repos'], queryFn: helmApi.listHelmRepos })
  const repos = reposQuery.data ?? []
  const repoId = Number(filters.repo) || repos[0]?.id
  const repo = repos.find((r) => r.id === repoId)
  const [ociChart, setOciChart] = useState('')
  const [preset, setPreset] = useState<ReleaseEditorPreset | null>(null)

  const firstRepoId = reposQuery.data?.[0]?.id
  useEffect(() => {
    if (!filters.repo && firstRepoId !== undefined) setFilters({ repo: String(firstRepoId) })
  }, [filters.repo, firstRepoId, setFilters])

  const chartsQuery = useQuery({
    queryKey: ['helm-charts', repoId, repo?.type === 'oci' ? ociChart : ''],
    queryFn: () => helmApi.searchCharts(repoId!, '', repo?.type === 'oci' ? { chart: ociChart } : {}),
    enabled: repoId !== undefined && (repo?.type !== 'oci' || ociChart.length > 0),
    staleTime: 5 * 60 * 1000,
  })

  const q = filters.q.trim().toLowerCase()
  const charts = useMemo(() => {
    const all = chartsQuery.data ?? []
    if (!q) return all
    return all.filter((c) => c.name.toLowerCase().includes(q) || (c.description ?? '').toLowerCase().includes(q) || (c.keywords ?? []).some((k) => k.toLowerCase().includes(q)))
  }, [chartsQuery.data, q])

  if (!validCluster) return <NotFound />

  return (
    <div>
      {preset && (
        <ReleaseEditorModal
          clusterId={clusterId}
          mode="install"
          preset={preset}
          defaultNamespace={namespaces.length === 1 ? namespaces[0] : undefined}
          onClose={() => setPreset(null)}
          onDone={() => {
            setPreset(null)
            navigate(`/clusters/${clusterId}/helm${filters.ns ? `?ns=${encodeURIComponent(filters.ns)}` : ''}`)
          }}
        />
      )}

      <PageHeader
        title={t('helm.catalogTitle')}
        description={t('helm.catalogHint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('helm.catalogTitle') }]}
        actions={
          user?.role === 'admin' ? (
            <Link to="/admin/helm-repos">
              <Button variant="secondary" size="sm">
                {t('helm.manageRepos')}
              </Button>
            </Link>
          ) : undefined
        }
      />

      {reposQuery.isError && <Alert tone="danger">{apiErrorText(reposQuery.error, t('helm.error'))}</Alert>}

      {repos.length === 0 && !reposQuery.isPending ? (
        <EmptyState icon={Package} title={t('helm.noRepos')} className="rounded-md border border-dashed border-border" />
      ) : (
        <>
          <Toolbar
            actions={
              <>
                <Button size="sm" variant="ghost" onClick={() => void chartsQuery.refetch()} title={t('helm.refreshIndex')}>
                  <RefreshCw className={`h-3.5 w-3.5 ${chartsQuery.isFetching ? 'animate-spin' : ''}`} aria-hidden="true" />
                </Button>
                {chartsQuery.data && <span className="text-xs text-fg-muted">{t('helm.chartCount', { count: charts.length })}</span>}
              </>
            }
          >
            <Select value={repoId ?? ''} onChange={(e) => setFilters({ repo: e.target.value, q: '' })} className="w-auto">
              {repos.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name} ({r.type})
                </option>
              ))}
            </Select>
            {repo?.type === 'oci' ? (
              <Input mono value={ociChart} onChange={(e) => setOciChart(e.target.value)} placeholder={t('helm.ociChartPlaceholder')} className="w-auto" />
            ) : (
              <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v })} placeholder={t('helm.searchChart')} />
            )}
            {q !== '' && (
              <Button size="sm" variant="ghost" onClick={() => setFilters({ q: '' })}>
                <X className="h-3.5 w-3.5" aria-hidden="true" />
                {t('common.clearFilters')}
              </Button>
            )}
          </Toolbar>

          {chartsQuery.isError && <Alert tone="danger">{apiErrorText(chartsQuery.error, t('helm.error'))}</Alert>}

          {chartsQuery.data && charts.length === 0 && (
            <EmptyState icon={Package} title={q ? t('common.noResults') : t('common.noData')} className="rounded-md border border-dashed border-border" />
          )}

          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            {chartsQuery.isPending && repoId !== undefined && (repo?.type !== 'oci' || ociChart) && (
              <p className="text-sm text-fg-muted">{t('common.loading')}</p>
            )}
            {charts.map((c) => (
              <Card key={c.name} className="flex min-w-0 flex-col p-4">
                <div className="flex items-start gap-3">
                  {c.icon ? (
                    <img src={c.icon} alt="" className="h-10 w-10 shrink-0 rounded object-contain" loading="lazy" />
                  ) : (
                    <Package className="h-10 w-10 shrink-0 text-fg-muted" aria-hidden="true" />
                  )}
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <h3 className="truncate font-mono text-sm font-semibold">{c.name}</h3>
                      {c.deprecated && <Badge tone="warning">{t('helm.deprecated')}</Badge>}
                    </div>
                    <p className="text-xs text-fg-muted">
                      {c.version}
                      {c.appVersion && ` · app ${c.appVersion}`}
                    </p>
                  </div>
                </div>
                {c.description && <p className="mt-2 line-clamp-3 flex-1 text-xs text-fg-muted">{c.description}</p>}
                <div className="mt-3 flex items-center justify-between gap-2">
                  {safeHttpUrl(c.home) ? (
                    <a href={safeHttpUrl(c.home)} target="_blank" rel="noreferrer" className="flex items-center gap-1 text-xs text-link hover:underline">
                      <ExternalLink className="h-3 w-3" aria-hidden="true" />
                      {t('helm.home')}
                    </a>
                  ) : (
                    <span />
                  )}
                  {canWrite && (
                    <Button size="sm" variant="primary" onClick={() => setPreset({ repoId, chart: c.name, version: c.version })}>
                      {t('helm.install')}
                    </Button>
                  )}
                </div>
              </Card>
            ))}
          </div>
        </>
      )}
    </div>
  )
}
