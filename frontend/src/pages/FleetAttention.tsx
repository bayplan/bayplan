import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { CheckCircle2 } from 'lucide-react'
import { getFleetAttention, type ClusterAttention } from '../api/fleet'
import { listClusters } from '../api/clusters'
import { explainKindFor } from '../api/ai'
import { ExplainButton } from '../components/ExplainButton'
import type { Issue } from '../api/resources'
import { Age, Alert, Badge, DataTable, PageHeader, StatusDot, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { apiErrorText } from '../lib/errors'

const POLL_INTERVAL_MS = 15000

/** An issue with the cluster it comes from, so several clusters can be mixed. */
interface FleetIssue extends Issue {
  clusterId: number
  cluster: string
}

export function FleetAttention() {
  const { t } = useTranslation()

  const query = useQuery({
    queryKey: ['fleet-attention'],
    queryFn: getFleetAttention,
    refetchInterval: POLL_INTERVAL_MS,
  })
  const clusters = query.data ?? []
  // No default order: the backend already prioritizes (errors first, the oldest
  // on top) and the merge below preserves it. Only the issues table is
  // paginated: the clusters one fits entirely.
  const [tableState, setTableState] = useTableState()
  const loadError = query.isError ? apiErrorText(query.error, t('common.error')) : undefined

  // The backend already prioritizes within each cluster; here the ones from
  // all of them are interleaved keeping that criterion (errors first, and within each
  // severity the oldest on top). It is done client-side because the backend answers per
  // cluster and this view is the only one that needs the merge.
  const issues: FleetIssue[] = clusters
    .flatMap((c) => c.issues.map((i) => ({ ...i, clusterId: c.clusterId, cluster: c.cluster })))
    .sort((a, b) => {
      if (a.severity !== b.severity) return a.severity === 'error' ? -1 : 1
      return new Date(a.since).getTime() - new Date(b.since).getTime()
    })
  const issuesTotal = clusters.reduce((sum, c) => sum + c.issuesTotal, 0)
  const disconnected = clusters.filter((c) => !c.connected)

  const reasonLabel = (reason: string) => t(`overview.reason.${reason}`, { defaultValue: reason })
  // Explain is offered per the user's role on EACH cluster, not the global one.
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const roleOf = (id: number) => clustersQuery.data?.find((c) => c.id === id)?.role

  const issueColumns: DataTableColumn<FleetIssue>[] = [
    {
      key: 'severity',
      header: '',
      cellClassName: 'w-4',
      render: (i) => (
        <StatusDot tone={i.severity === 'error' ? 'danger' : 'warning'} title={t(`overview.severity.${i.severity}`)} />
      ),
      primary: true,
    },
    {
      key: 'cluster',
      header: t('fleet.columns.cluster'),
      render: (i) => (
        <Link to={`/clusters/${i.clusterId}`} className="text-link hover:underline">
          {i.cluster}
        </Link>
      ),
      sortValue: (i) => i.cluster,
      primary: true,
    },
    {
      key: 'reason',
      header: t('overview.columns.problem'),
      primary: true,
      render: (i) => (
        <div>
          <span className="font-medium">{reasonLabel(i.reason)}</span>
          {i.detail && <span className="ml-2 text-xs text-fg-muted">{i.detail}</span>}
        </div>
      ),
      sortValue: (i) => i.reason,
    },
    {
      key: 'object',
      header: t('overview.columns.object'),
      cellClassName: 'font-mono text-xs',
      render: (i) => (i.namespace ? `${i.kind}/${i.namespace}/${i.name}` : `${i.kind}/${i.name}`),
      sortValue: (i) => `${i.kind}/${i.namespace ?? ''}/${i.name}`,
    },
    {
      key: 'since',
      header: t('overview.columns.since'),
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (i) => <Age createdAt={i.since} />,
      sortValue: (i) => new Date(i.since).getTime(),
    },
    {
      key: 'explain',
      header: '',
      cellClassName: 'w-px whitespace-nowrap text-right',
      render: (i) => {
        const kind = explainKindFor(i.kind)
        return kind ? <ExplainButton clusterId={i.clusterId} role={roleOf(i.clusterId)} kind={kind} namespace={i.namespace} name={i.name} variant="ghost" /> : null
      },
    },
  ]

  const clusterColumns: DataTableColumn<ClusterAttention>[] = [
    {
      key: 'cluster',
      header: t('fleet.columns.cluster'),
      cellClassName: 'font-mono',
      render: (c) => (
        <Link to={`/clusters/${c.clusterId}`} className="text-link hover:underline">
          {c.cluster}
        </Link>
      ),
      sortValue: (c) => c.cluster,
      primary: true,
    },
    {
      key: 'state',
      header: t('fleet.columns.state'),
      primary: true,
      render: (c) =>
        c.connected ? (
          <Badge tone={badgeToneFor(c)}>{t(`fleet.state.${stateFor(c)}`)}</Badge>
        ) : (
          // The informer's last error explains WHY it is not connected, which
          // is exactly what one previously had to go and dig out of the logs.
          <Badge tone="danger" className="max-w-72">
            <span className="truncate" title={c.lastError}>
              {t('common.connection.error')}
            </span>
          </Badge>
        ),
      sortValue: (c) => (c.connected ? stateFor(c) : 'zzz-disconnected'),
    },
    {
      key: 'version',
      header: t('cluster.columns.version'),
      cellClassName: 'text-fg-muted',
      render: (c) => c.kubeletVersion ?? '—',
      sortValue: (c) => c.kubeletVersion ?? '',
    },
    {
      key: 'nodes',
      header: t('cluster.tabs.nodes'),
      render: (c) => (c.connected ? c.nodes : '—'),
      sortValue: (c) => c.nodes,
    },
    {
      key: 'pods',
      header: t('cluster.tabs.pods'),
      render: (c) => c.counts?.pods.total ?? '—',
      sortValue: (c) => c.counts?.pods.total ?? -1,
    },
    {
      key: 'issues',
      header: t('fleet.columns.issues'),
      render: (c) =>
        c.issuesTotal === 0 ? (
          <span className="text-fg-muted">—</span>
        ) : (
          <Link to={`/clusters/${c.clusterId}`} className="text-link hover:underline">
            {c.issuesTotal}
          </Link>
        ),
      sortValue: (c) => -c.issuesTotal,
      primary: true,
    },
  ]

  return (
    <div>
      <PageHeader title={t('fleet.title')} description={t('fleet.subtitle')} />

      {/* Clusters that do not respond are highlighted at the top and not as one more
          row of the table: if one cannot look inside, the absence of issues in
          that cluster does not mean there are none. */}
      {disconnected.length > 0 && (
        <Alert tone="danger" className="mb-4">
          {t('fleet.disconnectedWarning', {
            count: disconnected.length,
            clusters: disconnected.map((c) => c.cluster).join(', '),
          })}
        </Alert>
      )}

      <section>
        <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold">
          {issues.length === 0 && <CheckCircle2 className="h-4 w-4 text-success" aria-hidden="true" />}
          {t('overview.attentionTitle')}
          {issuesTotal > 0 && ` (${issuesTotal})`}
        </h2>
        <DataTable
          columns={issueColumns}
          data={issues}
          rowKey={(i) => `${i.clusterId}/${i.kind}/${i.namespace ?? ''}/${i.name}/${i.reason}`}
          isLoading={query.isPending}
          error={loadError}
          emptyMessage={t('fleet.allGood')}
          state={tableState}
          onStateChange={setTableState}
        />
        {issuesTotal > issues.length && (
          <p className="mt-1 text-xs text-warning">
            {t('overview.issuesTruncated', { shown: issues.length, total: issuesTotal })}
          </p>
        )}
      </section>

      <section className="mt-6">
        <h2 className="mb-2 text-sm font-semibold">{t('fleet.clustersTitle')}</h2>
        <DataTable
          columns={clusterColumns}
          data={clusters}
          rowKey={(c) => String(c.clusterId)}
          isLoading={query.isPending}
          error={loadError}
          emptyMessage={t('clusterAdmin.empty')}
        />
      </section>
    </div>
  )
}

function stateFor(c: ClusterAttention): 'ok' | 'warning' | 'error' {
  if (c.issues.some((i) => i.severity === 'error')) return 'error'
  if (c.issuesTotal > 0) return 'warning'
  return 'ok'
}

function badgeToneFor(c: ClusterAttention) {
  const state = stateFor(c)
  return state === 'ok' ? 'success' : state === 'warning' ? 'warning' : 'danger'
}
