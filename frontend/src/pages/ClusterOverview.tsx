import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ExplainButton } from '../components/ExplainButton'
import { explainKindFor } from '../api/ai'
import { MetricsServerModal } from '../components/MetricsServerModal'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2 } from 'lucide-react'
import * as resources from '../api/resources'
import * as metricsApi from '../api/metrics'
import { listClusters } from '../api/clusters'
import { Age, Alert, Badge, Button, Card, DataTable, PageHeader, StatusDot, type BadgeTone, type DataTableColumn } from '../components/ui'
import { capacityTone, clusterConnection, connectionLabelKey, connectionTone } from '../lib/status'
import { formatBytes, formatCpu, percent } from '../lib/format'
import { apiErrorText } from '../lib/errors'
import { useTableState } from '../hooks/useTableState'
import { NotFound } from './NotFound'

const POLL_INTERVAL_MS = 15000

// Literal classes per tone (see ui/StatusDot.tsx): Tailwind v4 does not generate
// a class composed at runtime.
const GAUGE_STROKE: Record<BadgeTone, string> = {
  success: 'stroke-success',
  warning: 'stroke-warning',
  danger: 'stroke-danger',
  neutral: 'stroke-fg-muted',
}
const GAUGE_TEXT: Record<BadgeTone, string> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  neutral: 'text-fg-muted',
}

const GAUGE_RADIUS = 30
const GAUGE_CIRCUMFERENCE = 2 * Math.PI * GAUGE_RADIUS

/** Reserved capacity ring. Without metrics-server this is NOT real consumption. */
function CapacityGauge({
  label,
  used,
  total,
  text,
  free,
}: {
  label: string
  used: number
  total: number
  text: string
  free: string
}) {
  const pct = percent(used, total)
  const tone = capacityTone(pct)
  return (
    <div className="flex items-center gap-4 rounded-lg border border-border p-3">
      <div className="relative h-20 w-20 shrink-0">
        <svg viewBox="0 0 80 80" className="h-full w-full -rotate-90" aria-hidden="true">
          <circle cx="40" cy="40" r={GAUGE_RADIUS} fill="none" strokeWidth="8" className="stroke-surface-hover" />
          <circle
            cx="40"
            cy="40"
            r={GAUGE_RADIUS}
            fill="none"
            strokeWidth="8"
            strokeLinecap="round"
            strokeDasharray={GAUGE_CIRCUMFERENCE}
            // At 0 % the rounded cap would still paint a dot.
            strokeDashoffset={GAUGE_CIRCUMFERENCE * (1 - pct / 100)}
            className={`${GAUGE_STROKE[tone]} transition-[stroke-dashoffset] duration-500 ${pct === 0 ? 'opacity-0' : ''}`}
          />
        </svg>
        <span className={`absolute inset-0 flex items-center justify-center text-base font-semibold tabular-nums ${GAUGE_TEXT[tone]}`}>
          {Math.round(pct)}%
        </span>
      </div>
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        <div className="mt-0.5 truncate font-mono text-xs">{text}</div>
        <div className="mt-0.5 truncate text-xs text-fg-muted">{free}</div>
      </div>
    </div>
  )
}

/** Counter: total + how many are unhealthy. */
function CountCard({ label, count, to }: { label: string; count: resources.ResourceCount; to: string }) {
  return (
    <Link to={to} className="block">
      <Card className="p-4 transition-colors hover:border-primary">
        <p className="text-xs text-fg-muted">{label}</p>
        <p className="mt-1 text-2xl font-semibold">{count.total}</p>
        <div className="mt-2 flex flex-wrap gap-1">
          {/* The chips only appear if there is something to count: a healthy cluster
              does not fill up with zeros. */}
          {count.error > 0 && <Badge tone="danger">{count.error}</Badge>}
          {count.warning > 0 && <Badge tone="warning">{count.warning}</Badge>}
        </div>
      </Card>
    </Link>
  )
}

export function ClusterOverview() {
  const { id } = useParams<{ id: string }>()
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage ?? 'en'

  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0
  const [tableState, setTableState] = useTableState()

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)

  const connection = cluster ? clusterConnection(cluster) : 'unknown'

  const overviewQuery = useQuery({
    queryKey: ['overview', clusterId],
    queryFn: () => resources.getClusterOverview(clusterId),
    enabled: validCluster,
    refetchInterval: POLL_INTERVAL_MS,
  })
  const data = overviewQuery.data
  // Real consumption (metrics-server). Separate request from the overview: if there is
  // no metrics-server the response says so and the rest of the screen does not wait.
  const usageQuery = useQuery({
    queryKey: ['cluster-usage', clusterId],
    queryFn: () => metricsApi.getClusterUsage(clusterId),
    enabled: validCluster,
    refetchInterval: POLL_INTERVAL_MS,
  })
  const usage = usageQuery.data
  const [metricsModal, setMetricsModal] = useState(false)

  // The reasons are Kubernetes codes (CrashLoopBackOff, ImagePullBackOff…)
  // or our synthetic equivalents. The ones with a translation get translated and
  // the rest are shown as is: it is the vocabulary that appears in kubectl and in
  // the documentation, and translating it halfway would be worse than not translating it.
  const reasonLabel = (reason: string) => t(`overview.reason.${reason}`, { defaultValue: reason })

  const issueColumns: DataTableColumn<resources.Issue>[] = [
    {
      key: 'severity',
      header: '',
      cellClassName: 'w-4',
      render: (i) => <StatusDot tone={i.severity === 'error' ? 'danger' : 'warning'} title={t(`overview.severity.${i.severity}`)} />,
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
      primary: true,
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
        return kind ? <ExplainButton clusterId={clusterId} role={cluster?.role} kind={kind} namespace={i.namespace} name={i.name} variant="ghost" /> : null
      },
    },
  ]

  if (!validCluster) return <NotFound />

  const clusterName = cluster?.displayName ?? t('cluster.title')

  return (
    <div>
      <PageHeader
        title={t('overview.title')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: clusterName }]}
        description={
          connection === 'ok' || connection === 'unknown' ? undefined : (
            <Badge tone={connectionTone(connection)}>{t(connectionLabelKey(connection))}</Badge>
          )
        }
      />

      {overviewQuery.isPending && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
      {overviewQuery.isError && (
        <Alert tone="danger" title={t('overview.error')}>
          {apiErrorText(overviewQuery.error, t('common.error'))}
        </Alert>
      )}

      {data && (
        <div className="space-y-6">
          {/* Context: cheap to compute and answers "what is this" at a glance. */}
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
            <span>
              <span className="text-fg-muted">{t('overview.kubernetes')}: </span>
              <span className="font-mono">{data.kubeletVersion || '—'}</span>
            </span>
            <span>
              <span className="text-fg-muted">{t('overview.namespaces')}: </span>
              {data.namespaces}
            </span>
            {data.oldestNodeCreatedAt && (
              <span title={t('overview.oldestNodeHint')}>
                <span className="text-fg-muted">{t('overview.oldestNode')}: </span>
                <Age createdAt={data.oldestNodeCreatedAt} />
              </span>
            )}
            {/* A half-upgraded cluster is an operational problem that is
                worth seeing without having to look for it node by node. */}
            {data.kubeletVersions && (
              <Badge tone="warning">{t('overview.mixedVersions', { versions: data.kubeletVersions.join(', ') })}</Badge>
            )}
          </div>

          {/* StatefulSets and DaemonSets only appear if the cluster has any: in one
              that uses none, two cards at zero are noise. */}
          <div className="grid gap-3 sm:grid-cols-3 lg:grid-cols-5">
            <CountCard label={t('cluster.tabs.pods')} count={data.counts.pods} to={`/clusters/${clusterId}/pods`} />
            <CountCard
              label={t('cluster.tabs.deployments')}
              count={data.counts.deployments}
              to={`/clusters/${clusterId}/deployments`}
            />
            {data.counts.statefulsets?.total > 0 && (
              <CountCard
                label={t('cluster.tabs.statefulsets')}
                count={data.counts.statefulsets}
                to={`/clusters/${clusterId}/statefulsets`}
              />
            )}
            {data.counts.daemonsets?.total > 0 && (
              <CountCard
                label={t('cluster.tabs.daemonsets')}
                count={data.counts.daemonsets}
                to={`/clusters/${clusterId}/daemonsets`}
              />
            )}
            <CountCard label={t('cluster.tabs.nodes')} count={data.counts.nodes} to={`/clusters/${clusterId}/nodes`} />
          </div>

          <Card className="p-4">
            <div className="flex items-baseline justify-between gap-2">
              <h2 className="text-sm font-semibold">{t('overview.capacityTitle')}</h2>
              {/* A permanent notice and not a footnote: "reserved" and "used" get
                  confused constantly, and here only the former can be
                  known. */}
              <p className="text-xs text-fg-muted">{t('overview.capacityHint')}</p>
            </div>
            <div className="mt-3 grid gap-3 sm:grid-cols-3">
              <CapacityGauge
                label={t('overview.capacityPods')}
                used={data.capacity.podsUsed}
                total={data.capacity.podsAllocatable}
                text={`${data.capacity.podsUsed} / ${data.capacity.podsAllocatable}`}
                free={t('overview.capacityFree', {
                  value: Math.max(0, data.capacity.podsAllocatable - data.capacity.podsUsed),
                })}
              />
              <CapacityGauge
                label={t('overview.capacityCpu')}
                used={data.capacity.cpuRequestedMilli}
                total={data.capacity.cpuAllocatableMilli}
                text={`${formatCpu(data.capacity.cpuRequestedMilli, locale)} / ${formatCpu(data.capacity.cpuAllocatableMilli, locale)}`}
                free={t('overview.capacityFree', {
                  value: formatCpu(Math.max(0, data.capacity.cpuAllocatableMilli - data.capacity.cpuRequestedMilli), locale),
                })}
              />
              <CapacityGauge
                label={t('overview.capacityMemory')}
                used={data.capacity.memoryRequestedBytes}
                total={data.capacity.memoryAllocatableBytes}
                text={`${formatBytes(data.capacity.memoryRequestedBytes, locale)} / ${formatBytes(data.capacity.memoryAllocatableBytes, locale)}`}
                free={t('overview.capacityFree', {
                  value: formatBytes(
                    Math.max(0, data.capacity.memoryAllocatableBytes - data.capacity.memoryRequestedBytes),
                    locale,
                  ),
                })}
              />
            </div>
            <p className="mt-3 text-xs text-fg-muted">{t('overview.capacityLegend')}</p>
          </Card>

          {usage && (
            <Card className="p-4">
              <div className="flex items-baseline justify-between gap-2">
                <h2 className="text-sm font-semibold">{t('overview.usageTitle')}</h2>
                <p className="text-xs text-fg-muted">
                  {usage.available && usage.updatedAt
                    ? t('overview.usageUpdated', { when: new Date(usage.updatedAt).toLocaleTimeString(locale) })
                    : t('overview.usageHint')}
                </p>
              </div>
              {usage.available ? (
                <div className="mt-3 grid gap-3 sm:grid-cols-2">
                  <CapacityGauge
                    label={t('overview.capacityCpu')}
                    used={usage.totals.cpuUsedMilli}
                    total={usage.totals.cpuAllocatableMilli}
                    text={t('overview.usageOf', { used: formatCpu(usage.totals.cpuUsedMilli, locale), total: formatCpu(usage.totals.cpuAllocatableMilli, locale) })}
                    free={t('overview.usageReserved', { value: formatCpu(usage.totals.cpuRequestedMilli, locale) })}
                  />
                  <CapacityGauge
                    label={t('overview.capacityMemory')}
                    used={usage.totals.memoryUsedBytes}
                    total={usage.totals.memoryAllocatableBytes}
                    text={t('overview.usageOf', { used: formatBytes(usage.totals.memoryUsedBytes, locale), total: formatBytes(usage.totals.memoryAllocatableBytes, locale) })}
                    free={t('overview.usageReserved', { value: formatBytes(usage.totals.memoryRequestedBytes, locale) })}
                  />
                </div>
              ) : (
                <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
                  <p className="text-sm text-fg-muted">{t('overview.usageUnavailable', { reason: usage.reason })}</p>
                  {usage.reasonCode === 'NotInstalled' && cluster?.role === 'admin' && (
                    <Button size="sm" variant="primary" onClick={() => setMetricsModal(true)}>
                      {t('metricsServer.action')}
                    </Button>
                  )}
                </div>
              )}
            </Card>
          )}
          {metricsModal && <MetricsServerModal clusterId={clusterId} onClose={() => setMetricsModal(false)} />}

          <section>
            <div className="mb-2 flex items-baseline justify-between gap-2">
              <h2 className="flex items-center gap-2 text-sm font-semibold">
                {data.issues.length > 0 ? (
                  <AlertTriangle className="h-4 w-4 text-warning" aria-hidden="true" />
                ) : (
                  <CheckCircle2 className="h-4 w-4 text-success" aria-hidden="true" />
                )}
                {t('overview.attentionTitle')}
                {data.issuesTotal > 0 && ` (${data.issuesTotal})`}
              </h2>
              {/* If the list is trimmed it is said: a panel showing 50 of 400
                  issues without warning reads as "there are only 50". */}
              {data.issuesTotal > data.issues.length && (
                <p className="text-xs text-warning">
                  {t('overview.issuesTruncated', { shown: data.issues.length, total: data.issuesTotal })}
                </p>
              )}
            </div>
            {/* No default order: the backend already prioritizes them (errors
                first and, within each severity, the oldest on top), and
                reordering by default would throw that priority away. The headers remain
                sortable in case a specific object is being looked for. */}
            <DataTable
              columns={issueColumns}
              data={data.issues}
              rowKey={(i) => `${i.kind}/${i.namespace ?? ''}/${i.name}/${i.reason}`}
              emptyMessage={t('overview.allGood')}
              state={tableState}
              onStateChange={setTableState}
            />
          </section>

          {data.warningEvents.length > 0 && (
            <section>
              <div className="mb-2 flex items-baseline justify-between gap-2">
                <h2 className="text-sm font-semibold">{t('overview.warningEventsTitle')}</h2>
                <Link
                  to={`/clusters/${clusterId}/events?type=Warning`}
                  className="text-xs text-link hover:underline"
                >
                  {t('overview.seeAllEvents')}
                </Link>
              </div>
              <ul className="divide-y divide-border rounded-md border border-border">
                {data.warningEvents.map((e, i) => (
                  <li key={`${e.involvedObject}-${e.reason}-${i}`} className="px-3 py-2">
                    <div className="flex flex-wrap items-baseline gap-2 text-sm">
                      <span className="font-medium">{e.reason}</span>
                      <span className="font-mono text-xs text-fg-muted">{e.involvedObject}</span>
                      {/* The count is aggregated by the API server itself: one row with
                          "×33656" instead of 33656 identical rows. */}
                      {e.count > 1 && <span className="text-xs text-fg-muted">×{e.count}</span>}
                      <span className="ml-auto text-xs text-fg-muted">
                        <Age createdAt={e.lastSeen} />
                      </span>
                    </div>
                    <p className="mt-0.5 text-xs text-fg-muted">{e.message}</p>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>
      )}
    </div>
  )
}
