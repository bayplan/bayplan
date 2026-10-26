import { useMemo, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQueries, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Boxes, Gauge, LayoutGrid, List, Pencil, Plus, Server, Box, Eye } from 'lucide-react'
import * as clustersApi from '../api/clusters'
import * as resourcesApi from '../api/resources'
import * as fleetApi from '../api/fleet'
import * as metricsApi from '../api/metrics'
import * as dockerApi from '../api/docker'
import { SearchInput } from '../components/SearchInput'
import {
  Alert,
  Badge,
  Button,
  Card,
  cardClassName,
  DataTable,
  EmptyState,
  MenuItem,
  PageHeader,
  RowActions,
  SegmentedControl,
  Select,
  StatusDot,
  Toolbar,
  type BadgeTone,
  type DataTableColumn,
} from '../components/ui'
import { useAuth } from '../hooks/useAuth'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'
import { formatBytes, formatCpu, percent } from '../lib/format'
import { capacityTone, clusterConnection, connectionLabelKey, connectionTone, type ClusterConnection } from '../lib/status'
import { cn } from '../lib/cn'

// Refreshing the aggregated summary every few seconds is cheap: it comes from
// the backend's informer cache, it does not hit the cluster's API server.
const POLL_MS = 15000

type StateFilter = '' | 'connected' | 'issues' | 'disconnected'
type KindFilter = '' | 'kubernetes' | 'docker'
type View = 'cards' | 'table'

const FILTER_KEYS = ['q', 'state', 'kind', 'view'] as const

/** Per-cluster data that the cards and the table render the same way. */
interface ClusterRow {
  cluster: clustersApi.Cluster
  connection: ClusterConnection
  summary?: resourcesApi.ClusterSummary
  overview?: resourcesApi.ClusterOverview
  usage?: metricsApi.ClusterUsage
  docker?: dockerApi.DockerInfo
  attention?: fleetApi.ClusterAttention
}

function connectorLabelKey(c: clustersApi.Cluster): string {
  if (c.kind === 'docker') return 'dashboard.connector.docker'
  return `dashboard.connector.${c.connector}`
}

function stateOf(row: ClusterRow): StateFilter {
  if (row.connection === 'ok') return (row.attention?.issuesTotal ?? 0) > 0 ? 'issues' : 'connected'
  if (row.connection === 'connecting') return 'connected'
  return 'disconnected'
}

/**
 * Per-cluster load: summary (counters), overview (reserved capacity) and
 * real usage if metrics-server is there; a Docker host only asks for `docker info`. Three
 * requests per cluster every 15 s come from the informer cache or from the in-memory
 * snapshot of the metricsLoop: with dozens of clusters it is still cheap. With
 * `useQueries` the number of hooks does not depend on how many clusters there are.
 */
function useClusterRows(clusters: clustersApi.Cluster[], attentionById: Map<number, fleetApi.ClusterAttention>): ClusterRow[] {
  const live = clusters.filter((c) => c.enabled && c.kind !== 'docker')
  const dockers = clusters.filter((c) => c.enabled && c.kind === 'docker')
  const summaries = useQueries({
    queries: live.map((c) => ({
      queryKey: ['cluster-summary', c.id],
      queryFn: () => resourcesApi.getClusterSummary(c.id),
      refetchInterval: POLL_MS,
      retry: false,
    })),
  })
  const overviews = useQueries({
    queries: live.map((c) => ({
      queryKey: ['cluster-overview', c.id],
      queryFn: () => resourcesApi.getClusterOverview(c.id),
      refetchInterval: POLL_MS,
      retry: false,
    })),
  })
  const usages = useQueries({
    queries: live.map((c) => ({
      queryKey: ['cluster-usage', c.id],
      queryFn: () => metricsApi.getClusterUsage(c.id),
      refetchInterval: POLL_MS * 2,
      retry: false,
    })),
  })
  const infos = useQueries({
    queries: dockers.map((c) => ({
      queryKey: ['docker-info', c.id],
      queryFn: () => dockerApi.getDockerInfo(c.id),
      refetchInterval: POLL_MS,
      retry: false,
    })),
  })
  const liveIndex = new Map(live.map((c, i) => [c.id, i]))
  const dockerIndex = new Map(dockers.map((c, i) => [c.id, i]))
  return clusters.map((cluster) => {
    const i = liveIndex.get(cluster.id)
    const d = dockerIndex.get(cluster.id)
    return {
      cluster,
      connection: clusterConnection(cluster),
      summary: i !== undefined ? summaries[i]?.data : undefined,
      overview: i !== undefined ? overviews[i]?.data : undefined,
      usage: i !== undefined ? usages[i]?.data : undefined,
      docker: d !== undefined ? infos[d]?.data : undefined,
      attention: attentionById.get(cluster.id),
    }
  })
}

// Usage bar: reserved (requests) in a muted tone and, if metrics-server is there,
// real consumption on top in the tone's color. The label says which is which:
// confusing reserved with consumption misleads in both directions.
function UsageBar({ label, requested, used, allocatable, format }: { label: string; requested: number; used?: number; allocatable: number; format: (n: number) => string }) {
  const { t } = useTranslation()
  if (allocatable <= 0) return null
  // `percent` does not round (the overview formats it separately): here it is an integer.
  const reqPct = Math.round(percent(requested, allocatable))
  const usedPct = used !== undefined ? Math.round(percent(used, allocatable)) : undefined
  const tone = capacityTone(usedPct ?? reqPct)
  const fill: Record<BadgeTone, string> = {
    success: 'bg-success',
    warning: 'bg-warning',
    danger: 'bg-danger',
    neutral: 'bg-fg-muted',
  }
  const title =
    `${t('overview.capacityHint')}: ${format(requested)} / ${format(allocatable)} (${reqPct}%)` +
    (used !== undefined ? ` · ${t('dashboard.used')}: ${format(used)} (${usedPct}%)` : '')
  return (
    <div className="min-w-0" title={title}>
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="text-fg-muted">{label}</span>
        <span className="tabular-nums">
          {usedPct !== undefined ? `${usedPct}%` : `${reqPct}%`}
          <span className="text-fg-subtle"> {usedPct !== undefined ? t('dashboard.usedShort') : t('dashboard.reservedShort')}</span>
        </span>
      </div>
      <div className="relative mt-1 h-1.5 overflow-hidden rounded-full bg-neutral-soft">
        <span className="absolute inset-y-0 left-0 rounded-full bg-fg-subtle/40" style={{ width: `${Math.min(reqPct, 100)}%` }} />
        {usedPct !== undefined && <span className={cn('absolute inset-y-0 left-0 rounded-full', fill[tone])} style={{ width: `${Math.min(usedPct, 100)}%` }} />}
      </div>
    </div>
  )
}

// Each counter links to its listing (nodes, pods, deployments…): the number is
// the question and the list is the answer.
function Stat({ value, label, tone, to }: { value: ReactNode; label: string; tone?: BadgeTone; to?: string }) {
  const body = (
    <>
      <div className={cn('text-lg font-semibold tabular-nums', tone === 'warning' && 'text-warning', tone === 'danger' && 'text-danger')}>{value}</div>
      <div className="truncate text-xs text-fg-muted">{label}</div>
    </>
  )
  if (!to) return <div className="min-w-0 text-center">{body}</div>
  return (
    <Link to={to} className="min-w-0 rounded-md px-1 py-0.5 text-center transition-colors hover:bg-surface-hover [&>div:last-child]:hover:text-fg">
      {body}
    </Link>
  )
}

function ClusterMeta({ row }: { row: ClusterRow }) {
  const { t } = useTranslation()
  const c = row.cluster
  const version = c.status.serverVersion ?? row.overview?.kubeletVersion ?? row.docker?.serverVersion
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <Badge tone="neutral">{t(connectorLabelKey(c))}</Badge>
      {version && <Badge tone="neutral">{version}</Badge>}
      {c.rbacMode === 'cluster-admin' && c.kind !== 'docker' && <Badge tone="warning">cluster-admin</Badge>}
      {c.role && <Badge tone="neutral">{c.role}</Badge>}
    </div>
  )
}

function ClusterCard({ row, isAdmin }: { row: ClusterRow; isAdmin: boolean }) {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const c = row.cluster
  const s = row.summary
  const d = row.docker
  const cap = row.overview?.capacity
  const totals = row.usage?.available ? row.usage.totals : undefined
  const issues = row.attention?.issuesTotal ?? 0
  const href = `/clusters/${c.id}`
  const locale = i18n.language

  return (
    <Card className="flex min-w-0 flex-col p-5 transition-colors hover:border-primary">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <Link to={href} className="block truncate font-mono text-lg font-semibold text-fg hover:text-link hover:underline">
            {c.displayName}
          </Link>
          <p className="truncate text-xs text-fg-muted" title={c.kind === 'docker' ? d?.operatingSystem : c.apiEndpoint}>
            {c.kind === 'docker' ? (d ? `${d.operatingSystem}` : c.description || 'Docker') : c.apiEndpoint}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {issues > 0 && (
            <Link to={`/fleet/attention?q=${encodeURIComponent(c.displayName)}`} className="inline-flex" title={t('fleet.title')}>
              <Badge tone="danger">
                <AlertTriangle className="h-3 w-3" aria-hidden="true" />
                {issues}
              </Badge>
            </Link>
          )}
          <StatusDot tone={connectionTone(row.connection)} title={t(connectionLabelKey(row.connection))} />
          <RowActions label={t('common.actions')}>
            <MenuItem to={href}>
              <Gauge aria-hidden="true" />
              {t('overview.title')}
            </MenuItem>
            {c.kind !== 'docker' && (
              <MenuItem to={`${href}/pods`}>
                <Box aria-hidden="true" />
                {t('cluster.tabs.pods')}
              </MenuItem>
            )}
            {c.kind === 'docker' && (
              <MenuItem to={`${href}/containers`}>
                <Box aria-hidden="true" />
                {t('docker.containers')}
              </MenuItem>
            )}
            {isAdmin && (
              <MenuItem onSelect={() => navigate(`/admin/clusters?q=${encodeURIComponent(c.displayName)}`)}>
                <Pencil aria-hidden="true" />
                {t('dashboard.manage')}
              </MenuItem>
            )}
          </RowActions>
        </div>
      </div>

      <div className="mt-2">
        <ClusterMeta row={row} />
      </div>

      {(row.connection === 'error' || row.connection === 'disabled') && (
        <p className="mt-3">
          <Badge tone={connectionTone(row.connection)}>{t(`common.connection.${row.connection}`)}</Badge>
        </p>
      )}
      {row.connection === 'error' && c.status.lastError && (
        <p className="mt-1 line-clamp-2 text-xs text-danger" title={c.status.lastError}>
          {c.status.lastError}
        </p>
      )}

      {d && (
        <div className="mt-4 grid grid-cols-3 gap-2">
          <Stat value={d.containersRunning} label={t('docker.running')} to={`${href}/containers`} />
          <Stat value={d.containers} label={t('docker.containers')} to={`${href}/containers`} />
          <Stat value={d.images} label={t('docker.images')} to={`${href}/images`} />
        </div>
      )}
      {s && (
        <div className="mt-4 grid grid-cols-3 gap-2">
          <Stat value={s.nodes} label={t('dashboard.nodes')} tone={s.nodesNotReady > 0 ? 'danger' : undefined} to={`${href}/nodes`} />
          <Stat value={s.pods} label={t('dashboard.pods')} tone={s.podsNotReady > 0 ? 'warning' : undefined} to={`${href}/pods`} />
          <Stat value={s.deployments} label={t('dashboard.deployments')} tone={s.deploymentsNotReady > 0 ? 'warning' : undefined} to={`${href}/deployments`} />
        </div>
      )}
      {s && (s.podsNotReady > 0 || s.deploymentsNotReady > 0 || s.nodesNotReady > 0) && (
        <p className="mt-1 text-center text-xs text-warning">
          {t('dashboard.notReady', { count: s.podsNotReady + s.deploymentsNotReady + s.nodesNotReady })}
        </p>
      )}

      {cap && (
        <div className="mt-4 space-y-2">
          <UsageBar label={t('overview.capacityCpu')} requested={cap.cpuRequestedMilli} used={totals?.cpuUsedMilli} allocatable={cap.cpuAllocatableMilli} format={(n) => formatCpu(n, locale)} />
          <UsageBar label={t('overview.capacityMemory')} requested={cap.memoryRequestedBytes} used={totals?.memoryUsedBytes} allocatable={cap.memoryAllocatableBytes} format={(n) => formatBytes(n, locale)} />
          {row.usage && !row.usage.available && (
            <p className="text-[11px] text-fg-subtle">{t('dashboard.noMetrics')}</p>
          )}
        </div>
      )}
    </Card>
  )
}

function ClusterTable({ rows, isAdmin, filtered, loading }: { rows: ClusterRow[]; isAdmin: boolean; filtered: boolean; loading: boolean }) {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const locale = i18n.language
  const columns: DataTableColumn<ClusterRow>[] = [
    {
      key: 'state',
      header: t('clusterAdmin.enabled'),
      primary: true,
      render: (r) => (
        <span className="flex items-center gap-2">
          <StatusDot tone={connectionTone(r.connection)} title={t(connectionLabelKey(r.connection))} />
          <span className="text-xs text-fg-muted">{t(connectionLabelKey(r.connection))}</span>
        </span>
      ),
      sortValue: (r) => r.connection,
    },
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (r) => (
        <span className="block min-w-0">
          <Link to={`/clusters/${r.cluster.id}`} className="font-mono text-link hover:underline">
            {r.cluster.displayName}
          </Link>
          <span className="block truncate text-xs text-fg-muted">{r.cluster.kind === 'docker' ? 'Docker' : r.cluster.apiEndpoint}</span>
        </span>
      ),
      sortValue: (r) => r.cluster.displayName,
    },
    { key: 'connector', header: t('dashboard.connectorColumn'), render: (r) => t(connectorLabelKey(r.cluster)), sortValue: (r) => r.cluster.connector },
    { key: 'version', header: t('cluster.columns.version'), render: (r) => <span className="font-mono text-xs">{r.cluster.status.serverVersion ?? r.docker?.serverVersion ?? '—'}</span>, sortValue: (r) => r.cluster.status.serverVersion ?? '' },
    { key: 'nodes', header: t('dashboard.nodes'), render: (r) => r.summary?.nodes ?? (r.docker ? '—' : ''), sortValue: (r) => r.summary?.nodes ?? -1 },
    { key: 'pods', header: t('dashboard.pods'), render: (r) => r.summary?.pods ?? (r.docker ? `${r.docker.containersRunning}/${r.docker.containers}` : ''), sortValue: (r) => r.summary?.pods ?? -1 },
    {
      key: 'cpu',
      header: t('overview.capacityCpu'),
      cellClassName: 'w-40',
      render: (r) =>
        r.overview ? (
          <UsageBar label="" requested={r.overview.capacity.cpuRequestedMilli} used={r.usage?.available ? r.usage.totals.cpuUsedMilli : undefined} allocatable={r.overview.capacity.cpuAllocatableMilli} format={(n) => formatCpu(n, locale)} />
        ) : null,
      sortValue: (r) => (r.overview ? percent(r.overview.capacity.cpuRequestedMilli, r.overview.capacity.cpuAllocatableMilli) : -1),
    },
    {
      key: 'memory',
      header: t('overview.capacityMemory'),
      cellClassName: 'w-40',
      render: (r) =>
        r.overview ? (
          <UsageBar label="" requested={r.overview.capacity.memoryRequestedBytes} used={r.usage?.available ? r.usage.totals.memoryUsedBytes : undefined} allocatable={r.overview.capacity.memoryAllocatableBytes} format={(n) => formatBytes(n, locale)} />
        ) : null,
      sortValue: (r) => (r.overview ? percent(r.overview.capacity.memoryRequestedBytes, r.overview.capacity.memoryAllocatableBytes) : -1),
    },
    {
      key: 'issues',
      header: t('fleet.title'),
      primary: true,
      render: (r) => {
        const n = r.attention?.issuesTotal ?? 0
        return n > 0 ? (
          <Link to={`/fleet/attention?q=${encodeURIComponent(r.cluster.displayName)}`}>
            <Badge tone="danger">{n}</Badge>
          </Link>
        ) : r.connection === 'ok' ? (
          <Badge tone="success">0</Badge>
        ) : (
          '—'
        )
      },
      sortValue: (r) => r.attention?.issuesTotal ?? -1,
    },
  ]
  return (
    <DataTable
      columns={columns}
      data={rows}
      rowKey={(r) => String(r.cluster.id)}
      rowHref={(r) => `/clusters/${r.cluster.id}`}
      filtered={filtered}
      isLoading={loading}
      emptyMessage={t('dashboard.empty')}
      actions={(r) => (
        <RowActions>
          <MenuItem to={`/clusters/${r.cluster.id}`}>
            <Gauge aria-hidden="true" />
            {t('overview.title')}
          </MenuItem>
          <MenuItem to={r.cluster.kind === 'docker' ? `/clusters/${r.cluster.id}/containers` : `/clusters/${r.cluster.id}/pods`}>
            <Eye aria-hidden="true" />
            {r.cluster.kind === 'docker' ? t('docker.containers') : t('cluster.tabs.pods')}
          </MenuItem>
          {isAdmin && (
            <MenuItem onSelect={() => navigate(`/admin/clusters?q=${encodeURIComponent(r.cluster.displayName)}`)}>
              <Pencil aria-hidden="true" />
              {t('dashboard.manage')}
            </MenuItem>
          )}
        </RowActions>
      )}
    />
  )
}

function AddClusterCard() {
  const { t } = useTranslation()
  return (
    <Link
      to="/admin/clusters"
      className={cn(cardClassName, 'flex min-h-40 flex-col items-center justify-center gap-2 border-dashed p-5 text-fg-muted transition-colors hover:border-primary hover:text-fg')}
    >
      <Plus className="h-6 w-6" aria-hidden="true" />
      <span className="text-sm font-medium">{t('clusterAdmin.addTitle')}</span>
    </Link>
  )
}

/**
 * General dashboard, the panel's "Home". A strip with the fleet
 * totals, cards (or a table, for many clusters) with status, connection type,
 * version, counters, capacity and issues, and adding clusters one click away.
 * Until now it was a grid of cards with three counters and nothing else: the
 * first place a new user sees looked empty.
 */
export function Dashboard() {
  const { t } = useTranslation()
  const { user } = useAuth()
  const isAdmin = user?.role === 'admin'
  const [filters, setFilters] = useUrlState(FILTER_KEYS)

  // The connection status travels with each cluster, so refreshing the listing
  // is enough for the status dots to move on their own.
  const clustersQuery = useQuery({
    queryKey: ['clusters'],
    queryFn: clustersApi.listClusters,
    refetchInterval: POLL_MS,
  })
  // The fleet already trims to the N worst per cluster, but `issuesTotal` is the
  // real number: that is what gets shown, never the size of the list.
  const fleetQuery = useQuery({
    queryKey: ['fleet-attention'],
    queryFn: fleetApi.getFleetAttention,
    refetchInterval: POLL_MS,
    retry: false,
  })

  const clusters = clustersQuery.data ?? []
  const attentionById = useMemo(() => new Map((fleetQuery.data ?? []).map((a) => [a.clusterId, a])), [fleetQuery.data])
  const rows = useClusterRows(clusters, attentionById)

  return (
    <DashboardView
      rows={rows}
      isAdmin={isAdmin}
      loading={clustersQuery.isPending}
      error={clustersQuery.isError ? apiErrorText(clustersQuery.error, t('common.error')) : undefined}
      filters={filters}
      setFilters={setFilters}
      t={t}
    />
  )
}

function DashboardView({
  rows,
  isAdmin,
  loading,
  error,
  filters,
  setFilters,
  t,
}: {
  rows: ClusterRow[]
  isAdmin: boolean
  loading: boolean
  error?: string
  filters: Record<(typeof FILTER_KEYS)[number], string>
  setFilters: (patch: Partial<Record<(typeof FILTER_KEYS)[number], string>>) => void
  t: (k: string, o?: Record<string, unknown>) => string
}) {
  const view: View = filters.view === 'table' ? 'table' : 'cards'
  const state = (['connected', 'issues', 'disconnected'] as StateFilter[]).includes(filters.state as StateFilter) ? (filters.state as StateFilter) : ''
  const kind = filters.kind === 'kubernetes' || filters.kind === 'docker' ? (filters.kind as KindFilter) : ''
  const q = filters.q.trim().toLowerCase()
  const hasFilter = q !== '' || state !== '' || kind !== ''

  const visible = rows.filter((r) => {
    if (q && !r.cluster.displayName.toLowerCase().includes(q) && !(r.cluster.description ?? '').toLowerCase().includes(q)) return false
    if (state && stateOf(r) !== state) return false
    if (kind && r.cluster.kind !== kind) return false
    return true
  })

  // Fleet totals from what has already been requested per cluster: nothing new
  // against the server. What is not connected does not add up (unknown), and it is said.
  const totals = rows.reduce(
    (acc, r) => {
      acc.clusters += 1
      if (r.connection === 'ok') acc.connected += 1
      if (r.summary) {
        acc.nodes += r.summary.nodes
        acc.nodesNotReady += r.summary.nodesNotReady
        acc.pods += r.summary.pods
        acc.podsNotReady += r.summary.podsNotReady
      }
      acc.issues += r.attention?.issuesTotal ?? 0
      return acc
    },
    { clusters: 0, connected: 0, nodes: 0, nodesNotReady: 0, pods: 0, podsNotReady: 0, issues: 0 },
  )

  return (
    <div>
      <PageHeader
        title={t('dashboard.title')}
        actions={
          <>
            <Link to="/fleet/attention" className="inline-flex">
              <Button size="sm" variant={totals.issues > 0 ? 'secondary' : 'ghost'}>
                <AlertTriangle aria-hidden="true" className={totals.issues > 0 ? 'text-danger' : undefined} />
                {t('fleet.title')}
                {totals.issues > 0 && <Badge tone="danger">{totals.issues}</Badge>}
              </Button>
            </Link>
            {isAdmin && (
              <Link to="/admin/clusters" className="inline-flex">
                <Button size="sm" variant="primary">
                  <Plus aria-hidden="true" />
                  {t('clusterAdmin.addTitle')}
                </Button>
              </Link>
            )}
          </>
        }
      />

      {error && <Alert tone="danger">{error}</Alert>}

      {!error && rows.length > 0 && (
        <div className="mb-4 grid grid-cols-2 gap-3 md:grid-cols-4">
          <FleetStat icon={Server} label={t('dashboard.fleet.clusters')} value={`${totals.connected} / ${totals.clusters}`} hint={t('dashboard.fleet.connectedOf')} tone={totals.connected < totals.clusters ? 'warning' : undefined} />
          <FleetStat icon={Boxes} label={t('dashboard.nodes')} value={totals.nodes} hint={totals.nodesNotReady > 0 ? t('dashboard.fleet.notReady', { count: totals.nodesNotReady }) : undefined} tone={totals.nodesNotReady > 0 ? 'danger' : undefined} />
          <FleetStat icon={Box} label={t('dashboard.pods')} value={totals.pods} hint={totals.podsNotReady > 0 ? t('dashboard.fleet.notReady', { count: totals.podsNotReady }) : undefined} tone={totals.podsNotReady > 0 ? 'warning' : undefined} />
          <FleetStat icon={AlertTriangle} label={t('fleet.title')} value={totals.issues} to="/fleet/attention" tone={totals.issues > 0 ? 'danger' : 'success'} />
        </div>
      )}

      {!error && rows.length > 0 && (
        <Toolbar
          actions={
            <SegmentedControl
              size="sm"
              label={t('dashboard.view')}
              value={view}
              onChange={(v) => setFilters({ view: v === 'cards' ? '' : v })}
              options={[
                { value: 'cards', label: <LayoutGrid className="h-3.5 w-3.5" aria-label={t('dashboard.viewCards')} /> },
                { value: 'table', label: <List className="h-3.5 w-3.5" aria-label={t('dashboard.viewTable')} /> },
              ]}
            />
          }
        >
          <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v })} />
          <Select value={state} onChange={(e) => setFilters({ state: e.target.value })} className="w-auto" aria-label={t('clusterAdmin.enabled')}>
            <option value="">{t('dashboard.state.all')}</option>
            <option value="connected">{t('dashboard.state.connected')}</option>
            <option value="issues">{t('dashboard.state.issues')}</option>
            <option value="disconnected">{t('dashboard.state.disconnected')}</option>
          </Select>
          <Select value={kind} onChange={(e) => setFilters({ kind: e.target.value })} className="w-auto" aria-label={t('dashboard.kind.label')}>
            <option value="">{t('dashboard.kind.all')}</option>
            <option value="kubernetes">Kubernetes</option>
            <option value="docker">Docker</option>
          </Select>
          {hasFilter && (
            <Button size="sm" variant="ghost" onClick={() => setFilters({ q: '', state: '', kind: '' })}>
              {t('common.clearFilters')}
            </Button>
          )}
        </Toolbar>
      )}

      {!error && !loading && rows.length === 0 && (
        <Card className="p-8">
          <EmptyState
            icon={Server}
            title={t('dashboard.empty')}
            description={isAdmin ? t('dashboard.emptyAdminHint') : t('dashboard.emptyUserHint')}
            action={
              isAdmin ? (
                <Link to="/admin/clusters" className="inline-flex">
                  <Button variant="primary">
                    <Plus aria-hidden="true" />
                    {t('dashboard.addFirst')}
                  </Button>
                </Link>
              ) : undefined
            }
          />
          {isAdmin && (
            <div className="mx-auto mt-6 grid max-w-3xl gap-3 sm:grid-cols-3">
              {(['agent', 'direct', 'cloud'] as const).map((k) => (
                <div key={k} className="rounded-md border border-border p-4 text-sm">
                  <p className="font-medium">{t(`dashboard.connector.${k}`)}</p>
                  <p className="mt-1 text-xs text-fg-muted">{t(`dashboard.ways.${k}`)}</p>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}

      {!error && rows.length > 0 && view === 'table' && <ClusterTable rows={visible} isAdmin={isAdmin} filtered={hasFilter} loading={loading} />}

      {!error && rows.length > 0 && view === 'cards' && (
        <>
          {visible.length === 0 && <EmptyState title={t('common.noResults')} />}
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {visible.map((r) => (
              <ClusterCard key={r.cluster.id} row={r} isAdmin={isAdmin} />
            ))}
            {isAdmin && !hasFilter && <AddClusterCard />}
          </div>
        </>
      )}
    </div>
  )
}

function FleetStat({ icon: Icon, label, value, hint, tone, to }: { icon: typeof Server; label: string; value: ReactNode; hint?: string; tone?: BadgeTone; to?: string }) {
  const body = (
    <Card className={cn('flex items-center gap-3 p-4', to && 'transition-colors hover:border-primary')}>
      <span className={cn('flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-primary-soft text-primary', tone === 'danger' && 'bg-danger-soft text-danger', tone === 'warning' && 'bg-warning-soft text-warning', tone === 'success' && 'bg-success-soft text-success')}>
        <Icon className="h-4 w-4" aria-hidden="true" />
      </span>
      <span className="min-w-0">
        <span className="block text-xl font-semibold tabular-nums">{value}</span>
        <span className="block text-xs leading-tight text-fg-muted">
          {label}
          {hint && <span className={cn('ml-1', tone === 'danger' ? 'text-danger' : tone === 'warning' ? 'text-warning' : '')}>· {hint}</span>}
        </span>
      </span>
    </Card>
  )
  return to ? (
    <Link to={to} className="block min-w-0">
      {body}
    </Link>
  ) : (
    <div className="min-w-0">{body}</div>
  )
}
