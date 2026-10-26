import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FileCode, Gauge, Pencil, Plus, Trash2, X } from 'lucide-react'
import * as resources from '../api/resources'
import { listClusters } from '../api/clusters'
import { YamlEditor } from '../components/YamlEditor'
import { NamespaceQuotaModal } from '../components/NamespaceQuotaModal'
import { SearchInput } from '../components/SearchInput'
import { Age, Alert, Badge, Button, DataTable, MenuItem, PageHeader, RowActions, Toolbar, useConfirm, type BadgeTone, type DataTableColumn } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { useTableState } from '../hooks/useTableState'
import { apiErrorText, unavailableReason } from '../lib/errors'
import { NotFound } from './NotFound'

const POLL_INTERVAL_MS = 15000
const ICON = 'h-4 w-4 shrink-0'

/** Minimal row shared by all kinds: what the table needs for key, search and age. */
interface BaseRow {
  name: string
  namespace?: string
  createdAt: string
}

/** Kind-specific columns; name, namespace, age and actions are added by the page. */
type KindColumns<T extends BaseRow> = (t: TFunction) => DataTableColumn<T>[]

function badges(values: string[], tone: BadgeTone = 'neutral') {
  return (
    <span className="flex flex-wrap gap-1">
      {values.map((v) => (
        <Badge key={v} tone={tone}>
          {v}
        </Badge>
      ))}
    </span>
  )
}

/** An unknown state falls into warning, never into success (see lib/status.ts). */
function phaseTone(phase: string): BadgeTone {
  switch (phase) {
    case 'Bound':
    case 'Available':
    case 'Complete':
      return 'success'
    case 'Failed':
    case 'Lost':
      return 'danger'
    default:
      return 'warning'
  }
}

const configMapColumns: KindColumns<resources.ConfigMapSummary> = (t) => [
  { key: 'keys', header: t('resources.columns.keys'), render: (r) => badges(r.keys), sortValue: (r) => r.keys.length },
]

const secretColumns: KindColumns<resources.SecretSummary> = (t) => [
  { key: 'type', header: t('resources.columns.type'), cellClassName: 'font-mono text-xs', render: (r) => r.type, sortValue: (r) => r.type },
  { key: 'keys', header: t('resources.columns.keys'), render: (r) => badges(r.keys), sortValue: (r) => r.keys.length },
]

const serviceAccountColumns: KindColumns<resources.ServiceAccountSummary> = (t) => [
  { key: 'secrets', header: t('resources.columns.secrets'), render: (r) => r.secrets, sortValue: (r) => r.secrets },
]

const ingressColumns: KindColumns<resources.IngressSummary> = (t) => [
  { key: 'class', header: t('resources.columns.class'), render: (r) => r.class || '—', sortValue: (r) => r.class },
  {
    key: 'hosts',
    header: t('resources.columns.hosts'),
    render: (r) => (
      <div className="font-mono text-xs">
        {r.paths.length > 0 ? r.paths.map((p) => <div key={p}>{p}</div>) : r.hosts.join(', ')}
      </div>
    ),
    sortValue: (r) => r.hosts.join(','),
  },
  { key: 'tls', header: 'TLS', render: (r) => (r.tls ? <Badge tone="success">TLS</Badge> : '—'), sortValue: (r) => (r.tls ? 1 : 0) },
  { key: 'address', header: t('resources.columns.address'), cellClassName: 'font-mono text-xs', render: (r) => r.addresses.join(', ') || '—' },
]

const networkPolicyColumns: KindColumns<resources.NetworkPolicySummary> = (t) => [
  { key: 'selector', header: t('resources.columns.podSelector'), cellClassName: 'font-mono text-xs', render: (r) => r.podSelector },
  { key: 'types', header: t('resources.columns.policyTypes'), render: (r) => badges(r.policyTypes) },
]

const pvcColumns: KindColumns<resources.PVCSummary> = (t) => [
  { key: 'status', header: t('cluster.columns.status'), primary: true, render: (r) => <Badge tone={phaseTone(r.status)}>{r.status}</Badge>, sortValue: (r) => r.status },
  { key: 'capacity', header: t('resources.columns.capacity'), cellClassName: 'font-mono text-xs', render: (r) => r.capacity || '—', sortValue: (r) => r.capacity },
  { key: 'modes', header: t('resources.columns.accessModes'), render: (r) => badges(r.accessModes) },
  { key: 'sc', header: t('resources.columns.storageClass'), render: (r) => r.storageClass || '—', sortValue: (r) => r.storageClass },
  { key: 'volume', header: t('resources.columns.volume'), cellClassName: 'font-mono text-xs', render: (r) => r.volume || '—' },
]

const pvColumns: KindColumns<resources.PVSummary> = (t) => [
  { key: 'status', header: t('cluster.columns.status'), primary: true, render: (r) => <Badge tone={phaseTone(r.status)}>{r.status}</Badge>, sortValue: (r) => r.status },
  { key: 'capacity', header: t('resources.columns.capacity'), cellClassName: 'font-mono text-xs', render: (r) => r.capacity, sortValue: (r) => r.capacity },
  { key: 'modes', header: t('resources.columns.accessModes'), render: (r) => badges(r.accessModes) },
  { key: 'reclaim', header: t('resources.columns.reclaimPolicy'), render: (r) => r.reclaimPolicy, sortValue: (r) => r.reclaimPolicy },
  { key: 'sc', header: t('resources.columns.storageClass'), render: (r) => r.storageClass || '—', sortValue: (r) => r.storageClass },
  { key: 'claim', header: t('resources.columns.claim'), cellClassName: 'font-mono text-xs', render: (r) => r.claim || '—', sortValue: (r) => r.claim },
]

const storageClassColumns: KindColumns<resources.StorageClassSummary> = (t) => [
  { key: 'provisioner', header: t('resources.columns.provisioner'), cellClassName: 'font-mono text-xs', render: (r) => r.provisioner, sortValue: (r) => r.provisioner },
  { key: 'reclaim', header: t('resources.columns.reclaimPolicy'), render: (r) => r.reclaimPolicy, sortValue: (r) => r.reclaimPolicy },
  { key: 'binding', header: t('resources.columns.bindingMode'), render: (r) => r.volumeBindingMode },
  { key: 'default', header: '', render: (r) => (r.isDefault ? <Badge tone="success">{t('resources.default')}</Badge> : null), sortValue: (r) => (r.isDefault ? 0 : 1) },
]

const jobColumns: KindColumns<resources.JobSummary> = (t) => [
  {
    key: 'status',
    header: t('cluster.columns.status'),
    primary: true,
    render: (r) => <Badge tone={r.status === 'Running' ? 'warning' : phaseTone(r.status)}>{r.status}</Badge>,
    sortValue: (r) => r.status,
  },
  { key: 'completions', header: t('resources.columns.completions'), render: (r) => `${r.succeeded}/${r.completions}`, sortValue: (r) => (r.completions === 0 ? 1 : r.succeeded / r.completions) },
  { key: 'failed', header: t('resources.columns.failed'), render: (r) => (r.failed > 0 ? <span className="text-danger">{r.failed}</span> : 0), sortValue: (r) => r.failed },
  { key: 'duration', header: t('resources.columns.duration'), cellClassName: 'font-mono text-xs', render: (r) => r.duration ?? '—' },
]

const cronJobColumns: KindColumns<resources.CronJobSummary> = (t) => [
  { key: 'schedule', header: t('resources.columns.schedule'), cellClassName: 'font-mono text-xs', render: (r) => r.schedule, sortValue: (r) => r.schedule },
  { key: 'suspended', header: t('cluster.columns.status'), primary: true, render: (r) => (r.suspended ? <Badge tone="neutral">{t('resources.suspended')}</Badge> : <Badge tone="success">{t('resources.active')}</Badge>), sortValue: (r) => (r.suspended ? 1 : 0) },
  { key: 'active', header: t('resources.columns.activeJobs'), render: (r) => r.active, sortValue: (r) => r.active },
  { key: 'last', header: t('resources.columns.lastSchedule'), cellClassName: 'whitespace-nowrap text-fg-muted', render: (r) => (r.lastSchedule ? <Age createdAt={r.lastSchedule} /> : '—'), sortValue: (r) => (r.lastSchedule ? -new Date(r.lastSchedule).getTime() : 0) },
]

const hpaColumns: KindColumns<resources.HPASummary> = (t) => [
  { key: 'target', header: t('resources.columns.target'), cellClassName: 'font-mono text-xs', render: (r) => r.target, sortValue: (r) => r.target },
  { key: 'replicas', header: t('cluster.columns.replicas'), render: (r) => `${r.current} (${r.minReplicas}–${r.maxReplicas})`, sortValue: (r) => r.current },
  { key: 'metrics', header: t('resources.columns.metrics'), render: (r) => badges(r.metrics) },
]

/** used/hard bar per resource of a ResourceQuota; ≥90 % in red, ≥75 % in amber. */
function quotaBar(r: resources.QuotaResource) {
  const pct = r.percent < 0 ? 0 : Math.min(100, r.percent)
  const tone = r.percent >= 90 ? 'bg-danger' : r.percent >= 75 ? 'bg-warning' : 'bg-success'
  return (
    <div key={r.name} className="grid grid-cols-[minmax(7rem,auto)_1fr_auto] items-center gap-2 text-xs">
      <span className="font-mono text-fg-muted">{r.name}</span>
      <span className="h-1.5 w-full min-w-16 overflow-hidden rounded bg-surface-hover">
        <span className={`block h-full ${tone}`} style={{ width: `${pct}%` }} />
      </span>
      <span className="font-mono whitespace-nowrap">
        {r.used || '0'} / {r.hard}
      </span>
    </div>
  )
}

const resourceQuotaColumns: KindColumns<resources.ResourceQuotaSummary> = (t) => [
  {
    key: 'usage',
    header: t('resources.columns.usage'),
    render: (r) => (
      <div className="space-y-1">
        {r.resources.map(quotaBar)}
        {r.scopes.length > 0 && badges(r.scopes)}
        {r.managedByPanel && <Badge tone="neutral">{t('quota.managed')}</Badge>}
      </div>
    ),
    sortValue: (r) => Math.max(-1, ...r.resources.map((x) => x.percent)),
  },
]

const limitRangeColumns: KindColumns<resources.LimitRangeSummary> = (t) => [
  {
    key: 'limits',
    header: t('resources.columns.limits'),
    render: (r) => (
      <div className="space-y-0.5 font-mono text-xs">
        {r.limits.map((l) => (
          <div key={`${l.type}/${l.resource}`} className="flex flex-wrap gap-x-2">
            <span className="text-fg-muted">
              {l.type} {l.resource}:
            </span>
            {l.defaultRequest && <span>{t('quota.lr.defaultRequest')} {l.defaultRequest}</span>}
            {l.default && <span>{t('quota.lr.default')} {l.default}</span>}
            {l.min && <span>min {l.min}</span>}
            {l.max && <span>max {l.max}</span>}
          </div>
        ))}
        {r.managedByPanel && <Badge tone="neutral">{t('quota.managed')}</Badge>}
      </div>
    ),
  },
]

/** Detail page path; `-` as namespace for cluster-scoped kinds, same as in the API. */
function detailPath(clusterId: number, kind: resources.OptionalKind, r: BaseRow, view?: string) {
  const ns = resources.CLUSTER_SCOPED_KINDS.has(kind) || !r.namespace ? '-' : r.namespace
  return `/clusters/${clusterId}/${kind}/${ns}/${r.name}${view ? `/${view}` : ''}`
}

// Kind registry: the tab is resolved here, not with an if per kind. Adding
// a resource is adding an entry (plus its summary in the backend).
const KIND_COLUMNS: Record<resources.OptionalKind, KindColumns<BaseRow>> = {
  configmaps: configMapColumns as KindColumns<BaseRow>,
  secrets: secretColumns as KindColumns<BaseRow>,
  serviceaccounts: serviceAccountColumns as KindColumns<BaseRow>,
  ingresses: ingressColumns as KindColumns<BaseRow>,
  networkpolicies: networkPolicyColumns as KindColumns<BaseRow>,
  persistentvolumeclaims: pvcColumns as KindColumns<BaseRow>,
  persistentvolumes: pvColumns as KindColumns<BaseRow>,
  storageclasses: storageClassColumns as KindColumns<BaseRow>,
  jobs: jobColumns as KindColumns<BaseRow>,
  cronjobs: cronJobColumns as KindColumns<BaseRow>,
  horizontalpodautoscalers: hpaColumns as KindColumns<BaseRow>,
  resourcequotas: resourceQuotaColumns as KindColumns<BaseRow>,
  limitranges: limitRangeColumns as KindColumns<BaseRow>,
}


/**
 * Generic view for the secondary kinds (configmaps, ingresses, jobs…).
 * Same structure as ClusterView (filters in the URL, client-side search,
 * controlled table), but with the columns declared per kind instead of a
 * hand-written table per tab: ClusterView already has 900 lines with seven.
 */
export function ClusterResources({ kind }: { kind: resources.OptionalKind }) {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()

  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0
  const clusterScoped = resources.CLUSTER_SCOPED_KINDS.has(kind)

  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const currentCluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const isAdmin = currentCluster?.role === 'admin'
  const canWrite = isAdmin || currentCluster?.role === 'editor'
  const [actionError, setActionError] = useState<string | null>(null)

  const [filters, setFilters] = useUrlState(['ns', 'q'] as const)
  const namespaces = splitNamespaces(filters.ns)
  const query = filters.q.trim().toLowerCase()

  // Only "New resource": the YAML of an existing resource is its detail page.
  const [yamlTarget, setYamlTarget] = useState<{ namespace?: string } | null>(null)
  // Namespace quota form: only on the quotas and LimitRanges tabs.
  const quotaKind = kind === 'resourcequotas' || kind === 'limitranges'
  const [quotaTarget, setQuotaTarget] = useState<{ namespace?: string } | null>(null)
  const quotasQuery = useQuery({ queryKey: ['resourcequotas', clusterId, ''], queryFn: () => resources.listKind<resources.ResourceQuotaSummary>(clusterId, 'resourcequotas', []), enabled: validCluster && quotaTarget !== null, retry: false })
  const limitRangesQuery = useQuery({ queryKey: ['limitranges', clusterId, ''], queryFn: () => resources.listKind<resources.LimitRangeSummary>(clusterId, 'limitranges', []), enabled: validCluster && quotaTarget !== null, retry: false })
  const namespacesQuery = useQuery({ queryKey: ['namespaces', clusterId], queryFn: () => resources.listNamespaces(clusterId), enabled: validCluster && quotaTarget !== null })

  const deleteMutation = useMutation({
    mutationFn: ({ ns, name }: { ns?: string; name: string }) => resources.deleteResource(clusterId, kind, ns, name),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: [kind, clusterId] }),
    onError: (err) => setActionError(apiErrorText(err, t('cluster.actionError'))),
  })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })

  const listQuery = useQuery({
    queryKey: [kind, clusterId, clusterScoped ? '' : filters.ns],
    queryFn: () => resources.listKind<BaseRow>(clusterId, kind, namespaces),
    enabled: validCluster,
    refetchInterval: POLL_INTERVAL_MS,
    // A 503 caused by RBAC is not fixed by retrying every second; the polling
    // asks again anyway.
    retry: false,
  })

  const filtered = useMemo(() => {
    const items = listQuery.data ?? []
    if (!query) return items
    return items.filter((r) => r.name.toLowerCase().includes(query) || (r.namespace ?? '').toLowerCase().includes(query))
  }, [listQuery.data, query])

  const detailHref = (r: BaseRow, view?: string) => detailPath(clusterId, kind, r, view)

  const columns = useMemo<DataTableColumn<BaseRow>[]>(() => {
    const cols: DataTableColumn<BaseRow>[] = [
      {
        key: 'name',
        header: t('cluster.columns.name'),
        primary: true,
        render: (r) => (
          <Link to={detailPath(clusterId, kind, r)} className="font-mono text-link hover:underline">
            {r.name}
          </Link>
        ),
        sortValue: (r) => r.name,
      },
    ]
    if (!clusterScoped) {
      cols.push({ key: 'namespace', header: t('cluster.columns.namespace'), render: (r) => r.namespace ?? '', sortValue: (r) => r.namespace ?? '' })
    }
    cols.push(...KIND_COLUMNS[kind](t))
    cols.push({
      key: 'age',
      header: t('cluster.columns.age'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (r) => <Age createdAt={r.createdAt} />,
      sortValue: (r) => -new Date(r.createdAt).getTime(),
    })
    return cols
  }, [kind, clusterScoped, clusterId, t])

  const rowActions = (r: BaseRow) => (
    <RowActions>
      {quotaKind && canWrite && (r as unknown as { managedByPanel?: boolean }).managedByPanel && (
        <MenuItem onSelect={() => setQuotaTarget({ namespace: r.namespace })}>
          <Pencil className={ICON} aria-hidden="true" />
          {t('quota.edit')}
        </MenuItem>
      )}
      <MenuItem to={detailHref(r, 'yaml')}>
        <FileCode className={ICON} aria-hidden="true" />
        {t('cluster.viewYaml')}
      </MenuItem>
      {isAdmin && (
        <MenuItem
          danger
          onSelect={async () => {
            // Type the name, not an "OK": a confirm gets clicked without reading.
            if (!(await confirm({ title: t('cluster.delete'), description: t('cluster.confirmDelete', { name: r.name }), danger: true, typeToConfirm: r.name }))) return
            setActionError(null)
            deleteMutation.mutate({ ns: r.namespace, name: r.name })
          }}
        >
          <Trash2 className={ICON} aria-hidden="true" />
          {t('cluster.delete')}
        </MenuItem>
      )}
    </RowActions>
  )

  if (!validCluster) return <NotFound />

  // The 503 of an optional kind carries the reason (RBAC not reapplied): it is shown
  // as is, which is what the admin needs to read to fix it.
  const unavailable = listQuery.isError ? unavailableReason(listQuery.error, t('common.error')) : undefined

  const clusterName = currentCluster?.displayName ?? t('cluster.title')

  return (
    <div>
      {yamlTarget && (
        <YamlEditor
          clusterId={clusterId}
          namespace={yamlTarget.namespace}
          defaultNamespace={namespaces.length === 1 ? namespaces[0] : undefined}
          canWrite={canWrite}
          onClose={() => setYamlTarget(null)}
          onApplied={() => void queryClient.invalidateQueries({ queryKey: [kind, clusterId] })}
        />
      )}

      {quotaTarget && (
        <NamespaceQuotaModal
          clusterId={clusterId}
          namespaces={(namespacesQuery.data ?? []).map((n) => n.name)}
          initialNamespace={quotaTarget.namespace}
          quotas={quotasQuery.data ?? []}
          limitRanges={limitRangesQuery.data ?? []}
          onClose={() => setQuotaTarget(null)}
          onApplied={() => {
            void queryClient.invalidateQueries({ queryKey: ['resourcequotas', clusterId] })
            void queryClient.invalidateQueries({ queryKey: ['limitranges', clusterId] })
            void queryClient.invalidateQueries({ queryKey: ['namespaces', clusterId] })
          }}
        />
      )}

      <PageHeader
        title={t(`cluster.tabs.${kind}`)}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: clusterName, to: `/clusters/${clusterId}` }, { label: t(`cluster.tabs.${kind}`) }]}
        description={kind === 'secrets' ? t('resources.secretsHint') : quotaKind ? t('quota.pageHint') : undefined}
        actions={
          canWrite ? (
            <span className="inline-flex gap-2">
              {quotaKind && (
                <Button variant="primary" size="sm" onClick={() => setQuotaTarget({ namespace: namespaces.length === 1 ? namespaces[0] : undefined })}>
                  <Gauge className="h-3.5 w-3.5" aria-hidden="true" />
                  {t('quota.action')}
                </Button>
              )}
              <Button variant={quotaKind ? 'secondary' : 'primary'} size="sm" onClick={() => setYamlTarget({ namespace: namespaces.length === 1 ? namespaces[0] : undefined })}>
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                {t('yaml.newResource')}
              </Button>
            </span>
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
        <Alert tone="warning" title={t('resources.unavailable')}>
          <p className="break-all text-xs">{unavailable}</p>
          <p className="mt-1 text-xs">{t('resources.unavailableHint')}</p>
        </Alert>
      ) : (
        <DataTable
          columns={columns}
          data={filtered}
          rowKey={(r) => `${r.namespace ?? ''}/${r.name}`}
          rowHref={(r) => detailHref(r)}
          filtered={query !== ''}
          isLoading={listQuery.isPending}
          error={listQuery.isError ? apiErrorText(listQuery.error, t('common.error')) : undefined}
          actions={rowActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}
    </div>
  )
}
