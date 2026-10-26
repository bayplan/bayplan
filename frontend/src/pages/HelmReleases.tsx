import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Eye, Package, X } from 'lucide-react'
import * as helmApi from '../api/helm'
import { listClusters } from '../api/clusters'
import { SearchInput } from '../components/SearchInput'
import { ReleaseEditorModal } from '../components/helm/ReleaseEditorModal'
import { Age, Alert, Badge, Button, DataTable, MenuItem, Modal, PageHeader, RowActions, Tabs, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { useTableState } from '../hooks/useTableState'
import { helmReleaseTone } from '../lib/status'
import { NotFound } from './NotFound'
import { apiErrorText, httpStatus } from '../lib/errors'

const POLL_INTERVAL_MS = 15000


/**
 * Helm releases of the cluster. They are listed live (Helm keeps the state in
 * Secrets; there is no informer): a release installed from the CLI shows up just the same.
 */
export function HelmReleases() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const isAdmin = cluster?.role === 'admin'
  const canWrite = isAdmin || cluster?.role === 'editor'

  const [filters, setFilters] = useUrlState(['ns', 'q'] as const)
  const namespaces = splitNamespaces(filters.ns)
  const query = filters.q.trim().toLowerCase()
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [detail, setDetail] = useState<{ namespace: string; name: string } | null>(null)
  const [upgradeTarget, setUpgradeTarget] = useState<helmApi.ReleaseDetail | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  const releasesQuery = useQuery({
    queryKey: ['helm-releases', clusterId, filters.ns],
    queryFn: () => helmApi.listReleases(clusterId, namespaces),
    enabled: validCluster,
    refetchInterval: POLL_INTERVAL_MS,
    retry: false,
  })
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['helm-releases', clusterId] })

  const uninstallMutation = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) => helmApi.uninstallRelease(clusterId, ns, name),
    onSuccess: () => {
      invalidate()
      setDetail(null)
    },
    onError: (err) => setActionError(apiErrorText(err, t('helm.error'))),
  })

  const filtered = useMemo(() => {
    const items = releasesQuery.data ?? []
    if (!query) return items
    return items.filter((r) => r.name.toLowerCase().includes(query) || r.namespace.toLowerCase().includes(query) || r.chart.toLowerCase().includes(query))
  }, [releasesQuery.data, query])

  const columns: DataTableColumn<helmApi.ReleaseSummary>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (r) => (
        <button type="button" onClick={() => setDetail({ namespace: r.namespace, name: r.name })} className="font-mono text-link hover:underline">
          {r.name}
        </button>
      ),
      sortValue: (r) => r.name,
    },
    { key: 'namespace', header: t('cluster.columns.namespace'), render: (r) => r.namespace, sortValue: (r) => r.namespace },
    {
      key: 'chart',
      header: t('helm.chart'),
      primary: true,
      cellClassName: 'font-mono text-xs',
      render: (r) => `${r.chart}-${r.chartVersion}`,
      sortValue: (r) => r.chart,
    },
    { key: 'app', header: t('helm.appVersion'), cellClassName: 'text-xs', render: (r) => r.appVersion || '—', sortValue: (r) => r.appVersion },
    { key: 'revision', header: t('helm.revision'), render: (r) => r.revision, sortValue: (r) => r.revision },
    {
      key: 'status',
      header: t('cluster.columns.status'),
      primary: true,
      render: (r) => <Badge tone={helmReleaseTone(r.status)}>{r.status}</Badge>,
      sortValue: (r) => r.status,
    },
    {
      key: 'updated',
      header: t('helm.updated'),
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (r) => <Age createdAt={r.updated} />,
      sortValue: (r) => -new Date(r.updated).getTime(),
    },
  ]

  const rowActions = (r: helmApi.ReleaseSummary) => (
    <RowActions>
      <MenuItem onSelect={() => setDetail({ namespace: r.namespace, name: r.name })}>
        <Eye className="h-4 w-4 shrink-0" aria-hidden="true" />
        {t('helm.details')}
      </MenuItem>
    </RowActions>
  )

  if (!validCluster) return <NotFound />

  const unavailable =
    releasesQuery.isError && httpStatus(releasesQuery.error) === 502
      ? apiErrorText(releasesQuery.error, t('helm.error'))
      : undefined

  return (
    <div>
      {detail && (
        <ReleaseDetailModal
          clusterId={clusterId}
          namespace={detail.namespace}
          name={detail.name}
          canWrite={!!canWrite}
          isAdmin={!!isAdmin}
          onClose={() => setDetail(null)}
          onUpgrade={(d) => {
            setDetail(null)
            setUpgradeTarget(d)
          }}
          onUninstall={async (d) => {
            if (!(await confirm({ title: t('helm.uninstall'), description: t('helm.confirmUninstall', { name: d.name }), danger: true, typeToConfirm: d.name }))) return
            setActionError(null)
            uninstallMutation.mutate({ ns: d.namespace, name: d.name })
          }}
          onChanged={invalidate}
        />
      )}
      {upgradeTarget && (
        <ReleaseEditorModal
          clusterId={clusterId}
          mode="upgrade"
          existing={upgradeTarget}
          onClose={() => setUpgradeTarget(null)}
          onDone={() => {
            setUpgradeTarget(null)
            invalidate()
          }}
        />
      )}

      <PageHeader
        title={t('helm.releasesTitle')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('helm.releasesTitle') }]}
        actions={
          canWrite ? (
            <Link to={`/clusters/${clusterId}/catalog${filters.ns ? `?ns=${encodeURIComponent(filters.ns)}` : ''}`}>
              <Button variant="primary" size="sm">
                <Package className="h-3.5 w-3.5" aria-hidden="true" />
                {t('helm.installFromCatalog')}
              </Button>
            </Link>
          ) : undefined
        }
      />

      {actionError && (
        <Alert tone="danger" onClose={() => setActionError(null)} className="mb-3">
          {actionError}
        </Alert>
      )}

      <Toolbar>
        <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v })} placeholder={t('common.searchPlaceholder')} />
        {query !== '' && (
          <Button size="sm" variant="ghost" onClick={() => setFilters({ q: '' })}>
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>

      {unavailable ? (
        <Alert tone="warning">{unavailable}</Alert>
      ) : (
        <DataTable
          columns={columns}
          data={filtered}
          rowKey={(r) => `${r.namespace}/${r.name}`}
          filtered={query !== ''}
          isLoading={releasesQuery.isPending}
          error={releasesQuery.isError ? apiErrorText(releasesQuery.error, t('helm.error')) : undefined}
          emptyMessage={t('helm.noReleases')}
          actions={rowActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}
    </div>
  )
}

function ReleaseDetailModal({
  clusterId,
  namespace,
  name,
  canWrite,
  isAdmin,
  onClose,
  onUpgrade,
  onUninstall,
  onChanged,
}: {
  clusterId: number
  namespace: string
  name: string
  canWrite: boolean
  isAdmin: boolean
  onClose: () => void
  onUpgrade: (d: helmApi.ReleaseDetail) => void
  onUninstall: (d: helmApi.ReleaseDetail) => void
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const [tab, setTab] = useState<'notes' | 'values' | 'manifest' | 'history'>('history')
  const [error, setError] = useState<string | null>(null)
  const query = useQuery({
    queryKey: ['helm-release', clusterId, namespace, name],
    queryFn: () => helmApi.getRelease(clusterId, namespace, name),
  })
  const rollbackMutation = useMutation({
    mutationFn: (revision: number) => helmApi.rollbackRelease(clusterId, namespace, name, revision),
    onSuccess: () => {
      setError(null)
      void query.refetch()
      onChanged()
    },
    onError: (err) => setError(apiErrorText(err, t('helm.error'))),
  })
  const d = query.data

  return (
    <Modal
      onClose={onClose}
      size="xl"
      title={<span className="font-mono">{name}</span>}
      description={
        <>
          {namespace}
          {d && ` · ${d.chart}-${d.chartVersion} · rev ${d.revision}`}
        </>
      }
      footer={
        d ? (
          <>
            <span className="mr-auto self-center">
              <Badge tone={helmReleaseTone(d.status)}>{d.status}</Badge>
            </span>
            {canWrite && (
              <Button size="sm" onClick={() => onUpgrade(d)}>
                {t('helm.upgrade')}
              </Button>
            )}
            {isAdmin && (
              <Button size="sm" variant="danger" onClick={() => onUninstall(d)}>
                {t('helm.uninstall')}
              </Button>
            )}
          </>
        ) : undefined
      }
    >
      <Tabs
        items={[
          { key: 'history', label: t('helm.history') },
          { key: 'values', label: t('helm.values') },
          { key: 'manifest', label: t('helm.manifest') },
          { key: 'notes', label: t('helm.notes') },
        ]}
        active={tab}
        onChange={setTab}
      />

      <div className="mt-4 overflow-auto">
        {query.isPending && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
        {query.isError && <Alert tone="danger">{apiErrorText(query.error, t('helm.error'))}</Alert>}
        {error && (
          <Alert tone="danger" className="mb-2">
            {error}
          </Alert>
        )}
        {d && tab === 'history' && (
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-fg-muted uppercase">
              <tr>
                <th className="py-1 pr-3">{t('helm.revision')}</th>
                <th className="py-1 pr-3">{t('cluster.columns.status')}</th>
                <th className="py-1 pr-3">{t('helm.chart')}</th>
                <th className="py-1 pr-3">{t('helm.updated')}</th>
                <th className="py-1 pr-3">{t('helm.description')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {[...d.history].reverse().map((h) => (
                <tr key={h.revision} className="border-t border-border">
                  <td className="py-1.5 pr-3 font-mono">{h.revision}</td>
                  <td className="py-1.5 pr-3">
                    <Badge tone={helmReleaseTone(h.status)}>{h.status}</Badge>
                  </td>
                  <td className="py-1.5 pr-3 font-mono text-xs">
                    {h.chart}-{h.chartVersion}
                  </td>
                  <td className="py-1.5 pr-3 whitespace-nowrap text-fg-muted">
                    <Age createdAt={h.updated} />
                  </td>
                  <td className="py-1.5 pr-3 text-xs text-fg-muted">{h.description}</td>
                  <td className="py-1.5 text-right">
                    {canWrite && h.revision !== d.revision && (
                      <button
                        type="button"
                        onClick={async () => {
                          if (!(await confirm({ title: t('helm.confirmRollback', { name, revision: h.revision }), danger: true }))) return
                          rollbackMutation.mutate(h.revision)
                        }}
                        className="text-warning hover:underline"
                      >
                        {t('cluster.rollback')}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {d && tab === 'values' && <pre className="font-mono text-xs whitespace-pre">{d.values || t('helm.noValues')}</pre>}
        {d && tab === 'manifest' && <pre className="font-mono text-xs whitespace-pre">{d.manifest}</pre>}
        {d && tab === 'notes' && <pre className="text-xs whitespace-pre-wrap">{d.notes || '—'}</pre>}
      </div>
    </Modal>
  )
}
