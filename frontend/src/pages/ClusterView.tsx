import { useMemo, useState } from 'react'
import { Link, Navigate, useLocation, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import * as resources from '../api/resources'
import * as metricsApi from '../api/metrics'
import { listClusters } from '../api/clusters'
import { YamlEditor } from '../components/YamlEditor'
import { SearchInput } from '../components/SearchInput'
import { Ban, Boxes, CircleCheck, FileCode, Plus, RefreshCw, RotateCcw, Scaling, ScrollText, SquareTerminal, Trash2, Wind, X } from 'lucide-react'
import { Age, Alert, Badge, Button, DataTable, Input, MenuItem, PageHeader, RowActions, Select, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { capacityTone, clusterConnection, connectionTone, deploymentTone, nodeTone, podTone } from '../lib/status'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { useTableState } from '../hooks/useTableState'
import { formatBytes, formatCpu, percent } from '../lib/format'
import { apiErrorText } from '../lib/errors'
import { NotFound } from './NotFound'
import { isoFromDatetimeLocal } from '../lib/dates'

type Tab = 'pods' | 'deployments' | 'statefulsets' | 'daemonsets' | 'services' | 'nodes' | 'events'
const TABS: Tab[] = ['pods', 'deployments', 'statefulsets', 'daemonsets', 'services', 'nodes', 'events']
const DEFAULT_TAB: Tab = 'pods'

// The only vocabulary Event.Type accepts in the Kubernetes API. Needed as a
// list because the value comes from the query string and has to be validated.
const EVENT_TYPES: string[] = ['Normal', 'Warning']

// Names of the URL parameters. Short because they are visible, and in English
// like the rest of the project's identifiers and the API query params.
const FILTER_KEYS = ['ns', 'q', 'type', 'since', 'from', 'to', 'owner'] as const

// Reads served from the backend's informer cache: refreshing every few
// seconds is cheap and does not hit the managed cluster's API server.
const POLL_INTERVAL_MS = 15000

// Kinds created from the YAML editor ("New resource"): the namespaced ones.
// A node is not created by hand and neither is an event.
const CREATABLE_TABS: ReadonlySet<Tab> = new Set<Tab>(['pods', 'deployments', 'statefulsets', 'daemonsets', 'services'])

const ICON = 'h-4 w-4 shrink-0'

// Options of the date range selector in the Events tab. The absolute "since"
// value is recomputed on every query execution (not stored in state), so it
// stays fresh relative to the moment of the refetch.
type SinceOption = '' | '1h' | '6h' | '24h' | '7d'
const SINCE_MS: Record<Exclude<SinceOption, ''>, number> = {
  '1h': 60 * 60 * 1000,
  '6h': 6 * 60 * 60 * 1000,
  '24h': 24 * 60 * 60 * 1000,
  '7d': 7 * 24 * 60 * 60 * 1000,
}
// Takes a string and not a SinceOption on purpose: the value comes from the
// query string, i.e. user input. Without the check, a hand-written "?since=foo"
// gave SINCE_MS[foo] === undefined and new Date(NaN).toISOString() throws
// RangeError, taking down the whole page.
function sinceFromOption(option: string): string | undefined {
  const ms = SINCE_MS[option as Exclude<SinceOption, ''>]
  if (!ms) return undefined
  return new Date(Date.now() - ms).toISOString()
}

/**
 * Usage cell of a pod: what is used on the first line and the request below,
 * to see at a glance who is far above (or below) what they asked for. Without
 * metrics-server the reason goes in the title, "0" is not painted.
 */
function UsageCell({
  usage,
  list,
  value,
  request,
}: {
  usage: metricsApi.PodUsage | undefined
  list: metricsApi.PodUsageList | undefined
  value: (u: metricsApi.PodUsage) => string
  request: (u: metricsApi.PodUsage) => string
}) {
  const { t } = useTranslation()
  if (!list) return <span className="text-fg-muted">…</span>
  if (!list.available) {
    return (
      <span className="text-fg-muted" title={t('cluster.usageUnavailableShort', { reason: list.reason })}>
        —
      </span>
    )
  }
  if (!usage?.hasMetrics) return <span className="text-fg-muted">{t('cluster.usageNoData')}</span>
  const req = request(usage)
  return (
    <span className="flex flex-col leading-tight">
      <span>{value(usage)}</span>
      {req && <span className="text-fg-muted">{t('cluster.usageRequest', { value: req })}</span>}
    </span>
  )
}

/** Usage cell of a node: percentage over allocatable with a bar. */
function NodeUsageCell({
  usage,
  list,
  kind,
  locale,
}: {
  usage: metricsApi.NodeUsage | undefined
  list: metricsApi.ClusterUsage | undefined
  kind: 'cpu' | 'memory'
  locale: string
}) {
  const { t } = useTranslation()
  if (!list) return <span className="text-fg-muted">…</span>
  if (!list.available) {
    return (
      <span className="text-fg-muted" title={t('cluster.usageUnavailableShort', { reason: list.reason })}>
        —
      </span>
    )
  }
  if (!usage?.hasMetrics) return <span className="text-fg-muted">{t('cluster.usageNoData')}</span>
  const used = kind === 'cpu' ? usage.cpuUsedMilli : usage.memoryUsedBytes
  const total = kind === 'cpu' ? usage.cpuAllocatableMilli : usage.memoryAllocatableBytes
  const pct = percent(used, total)
  const text = kind === 'cpu' ? formatCpu(used, locale) : formatBytes(used, locale)
  const tone = capacityTone(pct)
  const bar = tone === 'danger' ? 'bg-danger' : tone === 'warning' ? 'bg-warning' : 'bg-success'
  return (
    <span className="flex min-w-24 flex-col gap-1">
      <span>
        {text} <span className="text-fg-muted">({Math.round(pct)} %)</span>
      </span>
      <span className="h-1 w-full overflow-hidden rounded bg-surface-hover">
        <span className={`block h-full ${bar}`} style={{ width: `${pct}%` }} />
      </span>
    </span>
  )
}

export function ClusterView() {
  const { id, tab: tabParam } = useParams<{ id: string; tab?: string }>()
  const location = useLocation()
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const confirm = useConfirm()

  // A non-numeric id (hand-written URL) is not a cluster: it resolves as 404
  // further down, and until then `validCluster` prevents the queries from
  // requesting /api/clusters/NaN/... and the page going blank unexplained.
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0
  const isKnownTab = TABS.includes(tabParam as Tab)
  const tab: Tab = isKnownTab ? (tabParam as Tab) : DEFAULT_TAB

  // EFFECTIVE role on THIS cluster (global + 'cluster' scope combined, see
  // clusterResponse.role in the backend) — not the session's global role,
  // which does not distinguish a 'cluster'-scoped admin from a global
  // readonly. Same query-key as Dashboard.tsx: React Query shares the cache.
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const currentCluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const isAdmin = currentCluster?.role === 'admin'
  // Editor and admin can operate workloads; delete, drain and the terminal
  // remain admin only.
  const canWrite = isAdmin || currentCluster?.role === 'editor'

  // Filters in the query string (see hooks/useUrlState): the whole view is a
  // shareable link and survives a reload. What stays in useState is ephemeral
  // interaction state (which panel is open, what is being typed into a
  // replicas input), which makes no sense to link to.
  const [filters, setFilters] = useUrlState(FILTER_KEYS)
  // The namespace filter is a list (see NamespacePicker); the query keys use it
  // as is, as a string, so every combination is a cache entry.
  const namespaces = splitNamespaces(filters.ns)
  const namespaceKey = filters.ns
  const searchTerm = filters.q
  // Event filters are normalised when read because they now come from the URL:
  // a value not in the list must behave as "no filter" and, above all, leave
  // the <select> consistent with what was actually applied.
  const eventType = EVENT_TYPES.includes(filters.type) ? filters.type : ''
  const eventSince = (Object.hasOwn(SINCE_MS, filters.since) ? filters.since : '') as SinceOption
  const eventSinceExact = filters.from
  const eventUntilExact = filters.to

  // "New resource" only: viewing or editing the YAML of an existing resource is
  // the detail page (/clusters/:id/:kind/:ns/:name/yaml), not a drawer.
  const [yamlTarget, setYamlTarget] = useState<{ namespace?: string } | null>(null)
  const [scaleDrafts, setScaleDrafts] = useState<Record<string, number>>({})

  // Connection state (travels with the cluster in /api/clusters). Without it, a
  // cluster that is down looks like an empty cluster: tables with 0 rows and
  // no explanation.
  const connection = currentCluster ? clusterConnection(currentCluster) : 'unknown'

  // `owner` narrows the pods to those of a specific deployment (reached from
  // its row with "View pods"). It goes in the URL like any other filter, so
  // that listing is a shareable link too.
  const owner = filters.owner
  const [ownerNs, ownerName] = owner ? owner.split('/', 2) : ['', '']
  const podsQuery = useQuery({
    queryKey: ['pods', clusterId, namespaceKey, owner],
    queryFn: () =>
      owner
        ? resources.listDeploymentPods(clusterId, ownerNs, ownerName)
        : resources.listPods(clusterId, namespaces),
    enabled: validCluster && tab === 'pods',
    refetchInterval: POLL_INTERVAL_MS,
  })
  const deploymentsQuery = useQuery({
    queryKey: ['deployments', clusterId, namespaceKey],
    queryFn: () => resources.listDeployments(clusterId, namespaces),
    enabled: validCluster && tab === 'deployments',
    refetchInterval: POLL_INTERVAL_MS,
  })
  const statefulSetsQuery = useQuery({
    queryKey: ['statefulsets', clusterId, namespaceKey],
    queryFn: () => resources.listStatefulSets(clusterId, namespaces),
    enabled: validCluster && tab === 'statefulsets',
    refetchInterval: POLL_INTERVAL_MS,
  })
  const daemonSetsQuery = useQuery({
    queryKey: ['daemonsets', clusterId, namespaceKey],
    queryFn: () => resources.listDaemonSets(clusterId, namespaces),
    enabled: validCluster && tab === 'daemonsets',
    refetchInterval: POLL_INTERVAL_MS,
  })
  const servicesQuery = useQuery({
    queryKey: ['services', clusterId, namespaceKey],
    queryFn: () => resources.listServices(clusterId, namespaces),
    enabled: validCluster && tab === 'services',
    refetchInterval: POLL_INTERVAL_MS,
  })
  const nodesQuery = useQuery({
    queryKey: ['nodes', clusterId],
    queryFn: () => resources.listNodes(clusterId),
    enabled: validCluster && tab === 'nodes',
    refetchInterval: POLL_INTERVAL_MS,
  })
  // Real usage (metrics-server) for the CPU/Memory columns of pods and nodes.
  // Separate queries: without metrics-server the listings do not wait.
  const podUsageQuery = useQuery({
    queryKey: ['pod-usage', clusterId, namespaceKey],
    queryFn: () => metricsApi.getPodUsage(clusterId, namespaces),
    enabled: validCluster && tab === 'pods',
    refetchInterval: POLL_INTERVAL_MS,
  })
  const nodeUsageQuery = useQuery({
    queryKey: ['cluster-usage', clusterId],
    queryFn: () => metricsApi.getClusterUsage(clusterId),
    enabled: validCluster && tab === 'nodes',
    refetchInterval: POLL_INTERVAL_MS,
  })
  const podUsageByKey = useMemo(() => {
    const m = new Map<string, metricsApi.PodUsage>()
    for (const p of podUsageQuery.data?.pods ?? []) m.set(`${p.namespace}/${p.name}`, p)
    return m
  }, [podUsageQuery.data])
  const nodeUsageByName = useMemo(() => {
    const m = new Map<string, metricsApi.NodeUsage>()
    for (const n of nodeUsageQuery.data?.nodes ?? []) if (n.name) m.set(n.name, n)
    return m
  }, [nodeUsageQuery.data])
  const usageLocale = i18n.resolvedLanguage ?? 'en'
  const eventsQuery = useQuery({
    queryKey: ['events', clusterId, namespaceKey, eventType, eventSince, eventSinceExact, eventUntilExact],
    queryFn: () =>
      resources.listEvents(clusterId, namespaces, {
        type: eventType,
        since: isoFromDatetimeLocal(eventSinceExact) ?? sinceFromOption(eventSince),
        until: isoFromDatetimeLocal(eventUntilExact),
      }),
    enabled: validCluster && tab === 'events',
    refetchInterval: POLL_INTERVAL_MS,
  })

  // Client-side search: pods/deployments/services/nodes are loaded in full
  // without pagination, so filtering on the client is immediate. Events use a
  // server-side filter (see above) because the backend truncates to 200 before
  // the client sees them. A single term per view, not one per tab: switching
  // tabs changes the route and with it the `q` parameter.
  const query = searchTerm.trim().toLowerCase()

  const filteredPods = useMemo(() => {
    if (!query) return podsQuery.data
    return podsQuery.data?.filter(
      (p) => p.name.toLowerCase().includes(query) || p.namespace.toLowerCase().includes(query),
    )
  }, [podsQuery.data, query])

  const filteredDeployments = useMemo(() => {
    if (!query) return deploymentsQuery.data
    return deploymentsQuery.data?.filter(
      (d) => d.name.toLowerCase().includes(query) || d.namespace.toLowerCase().includes(query),
    )
  }, [deploymentsQuery.data, query])

  const filteredStatefulSets = useMemo(() => {
    if (!query) return statefulSetsQuery.data
    return statefulSetsQuery.data?.filter(
      (s) => s.name.toLowerCase().includes(query) || s.namespace.toLowerCase().includes(query),
    )
  }, [statefulSetsQuery.data, query])

  const filteredDaemonSets = useMemo(() => {
    if (!query) return daemonSetsQuery.data
    return daemonSetsQuery.data?.filter(
      (d) => d.name.toLowerCase().includes(query) || d.namespace.toLowerCase().includes(query),
    )
  }, [daemonSetsQuery.data, query])

  const filteredServices = useMemo(() => {
    if (!query) return servicesQuery.data
    return servicesQuery.data?.filter(
      (s) => s.name.toLowerCase().includes(query) || s.namespace.toLowerCase().includes(query),
    )
  }, [servicesQuery.data, query])

  const filteredNodes = useMemo(() => {
    if (!query) return nodesQuery.data
    return nodesQuery.data?.filter((n) => n.name.toLowerCase().includes(query))
  }, [nodesQuery.data, query])

  // Actions return the API server error as is (cluster RBAC, admission
  // webhook…): it is what the operator needs to read.
  const [actionError, setActionError] = useState<string | null>(null)
  const onActionError = (err: unknown) =>
    setActionError(apiErrorText(err, t('cluster.actionError')))
  const invalidateTab = () => void queryClient.invalidateQueries({ queryKey: [tab, clusterId] })

  const restartMutation = useMutation({
    mutationFn: ({ kind, ns, name }: { kind: resources.RestartableKind; ns: string; name: string }) =>
      resources.restartWorkload(clusterId, kind, ns, name),
    onSuccess: invalidateTab,
    onError: onActionError,
  })

  const scaleMutation = useMutation({
    mutationFn: ({ kind, ns, name, replicas }: { kind: resources.ScalableKind; ns: string; name: string; replicas: number }) =>
      resources.scaleWorkload(clusterId, kind, ns, name, replicas),
    onSuccess: invalidateTab,
    onError: onActionError,
  })

  const rollbackMutation = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) => resources.rollbackDeployment(clusterId, ns, name),
    onSuccess: invalidateTab,
    onError: onActionError,
  })

  const deleteMutation = useMutation({
    mutationFn: ({ kind, ns, name }: { kind: resources.YamlKind; ns?: string; name: string }) =>
      resources.deleteResource(clusterId, kind, ns, name),
    onSuccess: invalidateTab,
    onError: onActionError,
  })

  const cordonMutation = useMutation({
    mutationFn: ({ name, cordon }: { name: string; cordon: boolean }) => resources.cordonNode(clusterId, name, cordon),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['nodes', clusterId] }),
    onError: onActionError,
  })

  const drainMutation = useMutation({
    mutationFn: ({ name }: { name: string }) => resources.drainNode(clusterId, name),
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: ['nodes', clusterId] })
      void confirm({
        title: t('cluster.drain'),
        description: (
          <>
            {t('cluster.drainResult', { evicted: res.evicted.length, skipped: res.skipped.length, failed: res.failed.length })}
            {res.failed.length > 0 && <span className="mt-2 block font-mono text-xs whitespace-pre-line">{res.failed.join('\n')}</span>}
          </>
        ),
        noticeOnly: true,
      })
    },
    onError: onActionError,
  })

  async function handleRestart(kind: resources.RestartableKind, ns: string, name: string) {
    if (!(await confirm({ title: t('cluster.confirmRestart', { name }), danger: true }))) return
    setActionError(null)
    restartMutation.mutate({ kind, ns, name })
  }

  async function handleScale(kind: resources.ScalableKind, ns: string, name: string, replicas: number) {
    const message =
      replicas === 0 ? t('cluster.confirmScaleZero', { name }) : t('cluster.confirmScale', { name, replicas })
    if (!(await confirm({ title: message, danger: true }))) return
    setActionError(null)
    scaleMutation.mutate({ kind, ns, name, replicas })
  }

  async function handleRollback(ns: string, name: string) {
    if (!(await confirm({ title: t('cluster.confirmRollback', { name }), danger: true }))) return
    setActionError(null)
    rollbackMutation.mutate({ ns, name })
  }

  // Delete asks to type the name, like deleting a cluster: an "OK" confirm
  // gets clicked without reading.
  async function handleDelete(kind: resources.YamlKind, ns: string | undefined, name: string) {
    if (!(await confirm({ title: t('cluster.delete'), description: t('cluster.confirmDelete', { name }), danger: true, typeToConfirm: name }))) return
    setActionError(null)
    deleteMutation.mutate({ kind, ns, name })
  }

  function handleCordon(name: string, cordon: boolean) {
    setActionError(null)
    cordonMutation.mutate({ name, cordon })
  }

  async function handleDrain(name: string) {
    if (!(await confirm({ title: t('cluster.drain'), description: t('cluster.confirmDrain', { name }), danger: true, typeToConfirm: name }))) return
    setActionError(null)
    drainMutation.mutate({ name })
  }

  // Replicas draft shared by Deployments and StatefulSets: the input lives in
  // the row and "Scale" applies whatever was typed.
  const replicasDraft = (ns: string, name: string, current: number) => scaleDrafts[`${ns}/${name}`] ?? current
  const replicasInput = (ns: string, name: string, current: number) => {
    const key = `${ns}/${name}`
    return (
      <Input
        type="number"
        min={0}
        max={100}
        value={scaleDrafts[key] ?? current}
        onChange={(e) => setScaleDrafts((prev) => ({ ...prev, [key]: Number(e.target.value) }))}
        disabled={!canWrite}
        aria-label={t('cluster.columns.replicas')}
        className="h-8 w-16 px-2"
      />
    )
  }

  // Delete entry reused in several menus; admin only.
  const deleteItem = (kind: resources.YamlKind, ns: string | undefined, name: string) =>
    isAdmin ? (
      <MenuItem danger onSelect={() => void handleDelete(kind, ns, name)}>
        <Trash2 className={ICON} aria-hidden="true" />
        {t('cluster.delete')}
      </MenuItem>
    ) : null

  // Route of a resource's detail page; `-` as namespace for cluster-scoped
  // kinds (nodes), same as in the API.
  const detailHref = (kind: string, ns: string | undefined, name: string, view?: string) =>
    `/clusters/${clusterId}/${kind}/${ns || '-'}/${name}${view ? `/${view}` : ''}`
  const nameLink = (kind: string, ns: string | undefined, name: string) => (
    <Link to={detailHref(kind, ns, name)} className="font-mono text-link hover:underline">
      {name}
    </Link>
  )

  // The "there are no resources" vs. "the filter found nothing" distinction is
  // now resolved by DataTable from `filtered`, not by each page by hand.
  const eventsFiltered = !!(eventType || eventSince || eventSinceExact || eventUntilExact)

  // Sort and page, also in the URL. Events start with NO default sort: the
  // backend already returns them by lastSeen desc before truncating to 200,
  // and re-sorting them here would give a list that does not match that cut.
  // They are lost when switching tabs (goToTab only keeps `ns`), which is
  // right: each listing's columns are different.
  const [tableState, setTableState] = useTableState({ sortKey: tab === 'events' ? '' : 'name' })

  const podColumns: DataTableColumn<resources.PodSummary>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (p) => nameLink('pods', p.namespace, p.name),
      sortValue: (p) => p.name,
    },
    {
      key: 'namespace',
      header: t('cluster.columns.namespace'),
      render: (p) => p.namespace,
      sortValue: (p) => p.namespace,
    },
    {
      key: 'status',
      header: t('cluster.columns.status'),
      primary: true,
      render: (p) => <Badge tone={podTone(p.status)}>{p.status}</Badge>,
      sortValue: (p) => p.status,
    },
    // `ready` arrives from the backend already formatted ("2/2"): sorting by
    // that string means nothing, and parsing a presentation text to rebuild the
    // ratio is fragile. It stays unsortable until the DTO brings the two
    // numbers separately.
    { key: 'ready', header: t('cluster.columns.ready'), primary: true, render: (p) => p.ready },
    {
      key: 'restarts',
      header: t('cluster.columns.restarts'),
      render: (p) => p.restarts,
      sortValue: (p) => p.restarts,
    },
    {
      key: 'cpu',
      header: t('cluster.columns.cpu'),
      cellClassName: 'whitespace-nowrap text-xs',
      render: (p) => (
        <UsageCell
          usage={podUsageByKey.get(`${p.namespace}/${p.name}`)}
          list={podUsageQuery.data}
          value={(u) => formatCpu(u.cpuUsedMilli, usageLocale)}
          request={(u) => (u.cpuRequestMilli ? formatCpu(u.cpuRequestMilli, usageLocale) : '')}
        />
      ),
      sortValue: (p) => podUsageByKey.get(`${p.namespace}/${p.name}`)?.cpuUsedMilli ?? -1,
    },
    {
      key: 'memory',
      header: t('cluster.columns.memory'),
      cellClassName: 'whitespace-nowrap text-xs',
      render: (p) => (
        <UsageCell
          usage={podUsageByKey.get(`${p.namespace}/${p.name}`)}
          list={podUsageQuery.data}
          value={(u) => formatBytes(u.memoryUsedBytes, usageLocale)}
          request={(u) => (u.memoryRequestBytes ? formatBytes(u.memoryRequestBytes, usageLocale) : '')}
        />
      ),
      sortValue: (p) => podUsageByKey.get(`${p.namespace}/${p.name}`)?.memoryUsedBytes ?? -1,
    },
    {
      key: 'node',
      header: t('cluster.columns.node'),
      cellClassName: 'text-fg-muted',
      render: (p) => p.node,
      sortValue: (p) => p.node,
    },
    // Ascending = newest first: it is what whoever sorts a column called "Age"
    // from lowest to highest expects, hence the sign.
    {
      key: 'age',
      header: t('cluster.columns.age'),
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (p) => <Age createdAt={p.createdAt} />,
      sortValue: (p) => -new Date(p.createdAt).getTime(),
    },
  ]

  const podActions = (pod: resources.PodSummary) => (
    <RowActions>
      <MenuItem to={detailHref('pods', pod.namespace, pod.name, 'logs')}>
        <ScrollText className={ICON} aria-hidden="true" />
        {t('cluster.viewLogs')}
      </MenuItem>
      <MenuItem to={detailHref('pods', pod.namespace, pod.name, 'yaml')}>
        <FileCode className={ICON} aria-hidden="true" />
        {t('cluster.viewYaml')}
      </MenuItem>
      {/* The terminal is a write capability (it can run any command inside the
          container): it is only offered if the user has the admin role on THIS
          cluster (see clusterResponse.role), not the session's global role. */}
      {isAdmin && (
        <MenuItem to={detailHref('pods', pod.namespace, pod.name, 'terminal')}>
          <SquareTerminal className={ICON} aria-hidden="true" />
          {t('terminal.open')}
        </MenuItem>
      )}
      {deleteItem('pods', pod.namespace, pod.name)}
    </RowActions>
  )

  const deploymentColumns: DataTableColumn<resources.DeploymentSummary>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (d) => nameLink('deployments', d.namespace, d.name),
      sortValue: (d) => d.name,
    },
    {
      key: 'namespace',
      header: t('cluster.columns.namespace'),
      render: (d) => d.namespace,
      sortValue: (d) => d.namespace,
    },
    {
      key: 'ready',
      header: t('cluster.columns.ready'),
      primary: true,
      render: (d) => <Badge tone={deploymentTone(d)}>{`${d.readyReplicas}/${d.replicas}`}</Badge>,
      // Sorts by the ratio of ready replicas, not by the text: ascending leaves
      // the worst on top (0/3 before 2/3), which is the real reason someone
      // sorts this column. A deployment scaled to 0 is not broken, so it counts
      // as complete and not as 0/0.
      sortValue: (d) => (d.replicas === 0 ? 1 : d.readyReplicas / d.replicas),
    },
    {
      key: 'replicas',
      header: t('cluster.columns.replicas'),
      render: (d) => replicasInput(d.namespace, d.name, d.replicas),
    },
    {
      key: 'age',
      header: t('cluster.columns.age'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (d) => <Age createdAt={d.createdAt} />,
      sortValue: (d) => -new Date(d.createdAt).getTime(),
    },
  ]

  const deploymentActions = (d: resources.DeploymentSummary) => (
    <RowActions>
      {/* A deployment's pods are resolved in the backend by ownerReferences
          through its ReplicaSets; here it is only linked. */}
      <MenuItem to={`/clusters/${clusterId}/pods?owner=${encodeURIComponent(`${d.namespace}/${d.name}`)}`}>
        <Boxes className={ICON} aria-hidden="true" />
        {t('cluster.viewPods')}
      </MenuItem>
      <MenuItem to={detailHref('deployments', d.namespace, d.name, 'yaml')}>
        <FileCode className={ICON} aria-hidden="true" />
        {t('cluster.viewYaml')}
      </MenuItem>
      {canWrite && (
        <>
          <MenuItem onSelect={() => void handleScale('deployments', d.namespace, d.name, replicasDraft(d.namespace, d.name, d.replicas))}>
            <Scaling className={ICON} aria-hidden="true" />
            {t('cluster.scale')}
          </MenuItem>
          <MenuItem onSelect={() => void handleRestart('deployments', d.namespace, d.name)}>
            <RefreshCw className={ICON} aria-hidden="true" />
            {t('cluster.restart')}
          </MenuItem>
          <MenuItem onSelect={() => void handleRollback(d.namespace, d.name)}>
            <RotateCcw className={ICON} aria-hidden="true" />
            {t('cluster.rollback')}
          </MenuItem>
        </>
      )}
      {deleteItem('deployments', d.namespace, d.name)}
    </RowActions>
  )

  const statefulSetColumns: DataTableColumn<resources.StatefulSetSummary>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (s) => nameLink('statefulsets', s.namespace, s.name),
      sortValue: (s) => s.name,
    },
    {
      key: 'namespace',
      header: t('cluster.columns.namespace'),
      render: (s) => s.namespace,
      sortValue: (s) => s.namespace,
    },
    {
      key: 'ready',
      header: t('cluster.columns.ready'),
      primary: true,
      render: (s) => (
        <Badge tone={deploymentTone({ replicas: s.replicas, readyReplicas: s.readyReplicas })}>
          {`${s.readyReplicas}/${s.replicas}`}
        </Badge>
      ),
      sortValue: (s) => (s.replicas === 0 ? 1 : s.readyReplicas / s.replicas),
    },
    {
      key: 'replicas',
      header: t('cluster.columns.replicas'),
      render: (s) => replicasInput(s.namespace, s.name, s.replicas),
    },
    {
      key: 'age',
      header: t('cluster.columns.age'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (s) => <Age createdAt={s.createdAt} />,
      sortValue: (s) => -new Date(s.createdAt).getTime(),
    },
  ]

  const statefulSetActions = (s: resources.StatefulSetSummary) => (
    <RowActions>
      <MenuItem to={detailHref('statefulsets', s.namespace, s.name, 'yaml')}>
        <FileCode className={ICON} aria-hidden="true" />
        {t('cluster.viewYaml')}
      </MenuItem>
      {canWrite && (
        <>
          <MenuItem onSelect={() => void handleScale('statefulsets', s.namespace, s.name, replicasDraft(s.namespace, s.name, s.replicas))}>
            <Scaling className={ICON} aria-hidden="true" />
            {t('cluster.scale')}
          </MenuItem>
          <MenuItem onSelect={() => void handleRestart('statefulsets', s.namespace, s.name)}>
            <RefreshCw className={ICON} aria-hidden="true" />
            {t('cluster.restart')}
          </MenuItem>
        </>
      )}
      {deleteItem('statefulsets', s.namespace, s.name)}
    </RowActions>
  )

  const daemonSetColumns: DataTableColumn<resources.DaemonSetSummary>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (d) => nameLink('daemonsets', d.namespace, d.name),
      sortValue: (d) => d.name,
    },
    {
      key: 'namespace',
      header: t('cluster.columns.namespace'),
      render: (d) => d.namespace,
      sortValue: (d) => d.namespace,
    },
    {
      // "Ready nodes" and not "Ready": in a DaemonSet the unit is the node, not
      // the replica, and naming it like in a Deployment is misleading.
      key: 'ready',
      header: t('cluster.columns.nodesReady'),
      primary: true,
      render: (d) => (
        <Badge tone={deploymentTone({ replicas: d.desired, readyReplicas: d.ready })}>
          {`${d.ready}/${d.desired}`}
        </Badge>
      ),
      sortValue: (d) => (d.desired === 0 ? 1 : d.ready / d.desired),
    },
    {
      key: 'misscheduled',
      header: t('cluster.columns.misscheduled'),
      render: (d) =>
        d.misscheduled > 0 ? <Badge tone="warning">{d.misscheduled}</Badge> : <span>0</span>,
      sortValue: (d) => -d.misscheduled,
    },
    {
      key: 'age',
      header: t('cluster.columns.age'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (d) => <Age createdAt={d.createdAt} />,
      sortValue: (d) => -new Date(d.createdAt).getTime(),
    },
  ]

  const daemonSetActions = (d: resources.DaemonSetSummary) => (
    <RowActions>
      <MenuItem to={detailHref('daemonsets', d.namespace, d.name, 'yaml')}>
        <FileCode className={ICON} aria-hidden="true" />
        {t('cluster.viewYaml')}
      </MenuItem>
      {canWrite && (
        <MenuItem onSelect={() => void handleRestart('daemonsets', d.namespace, d.name)}>
          <RefreshCw className={ICON} aria-hidden="true" />
          {t('cluster.restart')}
        </MenuItem>
      )}
      {deleteItem('daemonsets', d.namespace, d.name)}
    </RowActions>
  )

  const serviceColumns: DataTableColumn<resources.ServiceSummary>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (s) => nameLink('services', s.namespace, s.name),
      sortValue: (s) => s.name,
    },
    {
      key: 'namespace',
      header: t('cluster.columns.namespace'),
      render: (s) => s.namespace,
      sortValue: (s) => s.namespace,
    },
    { key: 'type', header: t('cluster.columns.type'), primary: true, render: (s) => s.type, sortValue: (s) => s.type },
    {
      key: 'clusterIP',
      header: t('cluster.columns.clusterIP'),
      render: (s) => s.clusterIP,
      sortValue: (s) => s.clusterIP,
    },
    { key: 'ports', header: t('cluster.columns.ports'), render: (s) => s.ports.join(', ') },
    {
      key: 'age',
      header: t('cluster.columns.age'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (s) => <Age createdAt={s.createdAt} />,
      sortValue: (s) => -new Date(s.createdAt).getTime(),
    },
  ]

  const serviceActions = (s: resources.ServiceSummary) => (
    <RowActions>
      <MenuItem to={detailHref('services', s.namespace, s.name, 'yaml')}>
        <FileCode className={ICON} aria-hidden="true" />
        {t('cluster.viewYaml')}
      </MenuItem>
      {deleteItem('services', s.namespace, s.name)}
    </RowActions>
  )

  const nodeColumns: DataTableColumn<resources.NodeSummary>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (n) => nameLink('nodes', undefined, n.name),
      sortValue: (n) => n.name,
    },
    {
      key: 'status',
      header: t('cluster.columns.status'),
      primary: true,
      render: (n) => (
        <span className="flex gap-1">
          <Badge tone={nodeTone(n.status)}>{n.status}</Badge>
          {n.unschedulable && <Badge tone="warning">{t('cluster.cordoned')}</Badge>}
        </span>
      ),
      sortValue: (n) => n.status,
    },
    {
      key: 'roles',
      header: t('cluster.columns.roles'),
      render: (n) => n.roles.join(', '),
      sortValue: (n) => n.roles.join(', '),
    },
    {
      key: 'version',
      header: t('cluster.columns.version'),
      cellClassName: 'text-fg-muted',
      render: (n) => n.version,
      sortValue: (n) => n.version,
    },
    {
      key: 'cpu',
      header: t('cluster.columns.cpu'),
      cellClassName: 'whitespace-nowrap text-xs',
      render: (n) => <NodeUsageCell usage={nodeUsageByName.get(n.name)} list={nodeUsageQuery.data} kind="cpu" locale={usageLocale} />,
      sortValue: (n) => {
        const u = nodeUsageByName.get(n.name)
        return u?.hasMetrics ? percent(u.cpuUsedMilli, u.cpuAllocatableMilli) : -1
      },
    },
    {
      key: 'memory',
      header: t('cluster.columns.memory'),
      cellClassName: 'whitespace-nowrap text-xs',
      render: (n) => <NodeUsageCell usage={nodeUsageByName.get(n.name)} list={nodeUsageQuery.data} kind="memory" locale={usageLocale} />,
      sortValue: (n) => {
        const u = nodeUsageByName.get(n.name)
        return u?.hasMetrics ? percent(u.memoryUsedBytes, u.memoryAllocatableBytes) : -1
      },
    },
    {
      key: 'age',
      header: t('cluster.columns.age'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (n) => <Age createdAt={n.createdAt} />,
      sortValue: (n) => -new Date(n.createdAt).getTime(),
    },
  ]

  const nodeActions = (n: resources.NodeSummary) => (
    <RowActions>
      <MenuItem to={detailHref('nodes', undefined, n.name, 'yaml')}>
        <FileCode className={ICON} aria-hidden="true" />
        {t('cluster.viewYaml')}
      </MenuItem>
      {canWrite && (
        <MenuItem onSelect={() => handleCordon(n.name, !n.unschedulable)}>
          {n.unschedulable ? <CircleCheck className={ICON} aria-hidden="true" /> : <Ban className={ICON} aria-hidden="true" />}
          {n.unschedulable ? t('cluster.uncordon') : t('cluster.cordon')}
        </MenuItem>
      )}
      {isAdmin && (
        <MenuItem danger onSelect={() => void handleDrain(n.name)}>
          <Wind className={ICON} aria-hidden="true" />
          {t('cluster.drain')}
        </MenuItem>
      )}
    </RowActions>
  )

  const eventColumns: DataTableColumn<resources.EventSummary & { _key: string }>[] = [
    {
      key: 'lastSeen',
      header: t('cluster.columns.lastSeen'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted align-top',
      // Relative ("3 min") with the exact date in the tooltip, like the age.
      render: (e) => <Age createdAt={e.lastSeen} />,
      sortValue: (e) => new Date(e.lastSeen).getTime(),
    },
    {
      key: 'type',
      header: t('cluster.columns.type'),
      primary: true,
      cellClassName: 'align-top',
      render: (e) => (
        <Badge tone={e.type === 'Warning' ? 'warning' : 'neutral'}>
          {e.type === 'Normal' ? t('cluster.events.typeInfo') : e.type}
        </Badge>
      ),
      sortValue: (e) => e.type,
    },
    {
      key: 'reason',
      header: t('cluster.columns.reason'),
      cellClassName: 'align-top',
      render: (e) => e.reason,
      sortValue: (e) => e.reason,
    },
    {
      key: 'object',
      header: t('cluster.columns.object'),
      primary: true,
      cellClassName: 'font-mono align-top',
      render: (e) => e.involvedObject,
      sortValue: (e) => e.involvedObject,
    },
    { key: 'message', header: t('cluster.columns.message'), primary: true, cellClassName: 'align-top', render: (e) => e.message },
  ]

  if (!validCluster) return <NotFound />
  // Missing or unknown tab: normalised in the URL itself keeping the filters,
  // instead of rendering Pods under a URL that says something else. `replace`
  // so as not to leave in the history an entry the back button would return
  // to only to redirect again.
  if (!isKnownTab) {
    return (
      <Navigate
        to={{ pathname: `/clusters/${clusterId}/${DEFAULT_TAB}`, search: location.search }}
        replace
      />
    )
  }

  const clusterName = currentCluster?.displayName ?? t('cluster.title')
  const tabTitle = t(`cluster.tabs.${tab}`)
  const filtersActive = query !== '' || eventsFiltered || (tab === 'pods' && owner !== '')
  const loadError = (q: { isError: boolean; error: unknown }) => (q.isError ? apiErrorText(q.error, t('common.error')) : undefined)

  return (
    <div>
      {yamlTarget && (
        <YamlEditor
          clusterId={clusterId}
          namespace={yamlTarget.namespace}
          defaultNamespace={namespaces.length === 1 ? namespaces[0] : undefined}
          canWrite={canWrite}
          onClose={() => setYamlTarget(null)}
          onApplied={() => void queryClient.invalidateQueries({ queryKey: [tab, clusterId] })}
        />
      )}

      <PageHeader
        title={tabTitle}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: clusterName, to: `/clusters/${clusterId}` }, { label: tabTitle }]}
        // Only warn when there is something to warn about: a connected cluster
        // does not need a banner saying so, but one that is down does, because
        // otherwise the empty tables below have no explanation.
        description={
          connection === 'ok' || connection === 'unknown' ? undefined : (
            <Badge tone={connectionTone(connection)}>{t(`common.connection.${connection}`)}</Badge>
          )
        }
        // "New resource" only where something is created via YAML: not on nodes nor events.
        actions={
          canWrite && CREATABLE_TABS.has(tab) ? (
            <Button variant="primary" size="sm" onClick={() => setYamlTarget({ namespace: namespaces.length === 1 ? namespaces[0] : undefined })}>
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
              {t('yaml.newResource')}
            </Button>
          ) : undefined
        }
      />

      {actionError && (
        <Alert tone="danger" onClose={() => setActionError(null)} className="mb-3">
          {actionError}
        </Alert>
      )}

      <Toolbar>
        {tab !== 'events' && (
          <SearchInput
            value={searchTerm}
            onChange={(v) => setFilters({ q: v })}
            placeholder={t('common.searchPlaceholder')}
          />
        )}
        {tab === 'events' && (
          <>
            <Select value={eventType} onChange={(e) => setFilters({ type: e.target.value })} className="w-auto">
              <option value="">{t('cluster.events.allTypes')}</option>
              {/* value stays "Normal": it is the only real vocabulary the Kubernetes
                  API accepts for Event.Type — only the visible label changes. */}
              <option value="Normal">{t('cluster.events.typeInfo')}</option>
              <option value="Warning">{t('cluster.events.typeWarning')}</option>
            </Select>
            <Select
              value={eventSince}
              onChange={(e) => setFilters({ since: e.target.value, from: '', to: '' })}
              disabled={!!eventSinceExact || !!eventUntilExact}
              className="w-auto"
            >
              <option value="">{t('cluster.events.anyTime')}</option>
              <option value="1h">{t('cluster.events.last1h')}</option>
              <option value="6h">{t('cluster.events.last6h')}</option>
              <option value="24h">{t('cluster.events.last24h')}</option>
              <option value="7d">{t('cluster.events.last7d')}</option>
            </Select>
            <label className="flex items-center gap-1.5 text-xs text-fg-muted">
              {t('cluster.events.sinceLabel')}
              <Input type="datetime-local" value={eventSinceExact} onChange={(e) => setFilters({ from: e.target.value })} className="w-auto" />
            </label>
            <label className="flex items-center gap-1.5 text-xs text-fg-muted">
              {t('cluster.events.untilLabel')}
              <Input type="datetime-local" value={eventUntilExact} onChange={(e) => setFilters({ to: e.target.value })} className="w-auto" />
            </label>
            {eventsQuery.data?.truncated && (
              <span className="text-xs text-warning">{t('cluster.events.truncated')}</span>
            )}
          </>
        )}

        {/* A single reset button instead of a chip per filter: the controls
            already show their own value, so repeating them as labels would
            duplicate information on screen. What there was no way to do was
            going back to the unfiltered state at once. It does not clear `ns`:
            that is cluster context and lives in the topbar, not in this bar. */}
        {/* When arriving from a deployment's "View pods", it has to be visibly
            said that the list is narrowed: otherwise a listing with 3 of 340
            pods looks like information is missing. */}
        {tab === 'pods' && owner && (
          <Badge tone="neutral" className="gap-1">
            {t('cluster.ownedBy', { name: ownerName })}
            <button type="button" onClick={() => setFilters({ owner: '' })} title={t('common.clearFilters')}>
              <X className="h-3 w-3" aria-hidden="true" />
            </button>
          </Badge>
        )}

        {filtersActive && (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setFilters({ q: '', type: '', since: '', from: '', to: '', owner: '' })}
          >
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>

      {tab === 'pods' && (
        <DataTable
          columns={podColumns}
          data={filteredPods ?? []}
          rowKey={(p) => `${p.namespace}/${p.name}`}
          rowHref={(p) => detailHref('pods', p.namespace, p.name)}
          filtered={filtersActive}
          isLoading={podsQuery.isPending}
          error={loadError(podsQuery)}
          actions={podActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}

      {tab === 'deployments' && (
        <DataTable
          columns={deploymentColumns}
          data={filteredDeployments ?? []}
          rowKey={(d) => `${d.namespace}/${d.name}`}
          rowHref={(d) => detailHref('deployments', d.namespace, d.name)}
          filtered={query !== ''}
          isLoading={deploymentsQuery.isPending}
          error={loadError(deploymentsQuery)}
          actions={deploymentActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}

      {tab === 'statefulsets' && (
        <DataTable
          columns={statefulSetColumns}
          data={filteredStatefulSets ?? []}
          rowKey={(s) => `${s.namespace}/${s.name}`}
          rowHref={(s) => detailHref('statefulsets', s.namespace, s.name)}
          filtered={query !== ''}
          isLoading={statefulSetsQuery.isPending}
          error={loadError(statefulSetsQuery)}
          actions={statefulSetActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}

      {tab === 'daemonsets' && (
        <DataTable
          columns={daemonSetColumns}
          data={filteredDaemonSets ?? []}
          rowKey={(d) => `${d.namespace}/${d.name}`}
          rowHref={(d) => detailHref('daemonsets', d.namespace, d.name)}
          filtered={query !== ''}
          isLoading={daemonSetsQuery.isPending}
          error={loadError(daemonSetsQuery)}
          actions={daemonSetActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}

      {tab === 'services' && (
        <DataTable
          columns={serviceColumns}
          data={filteredServices ?? []}
          rowKey={(s) => `${s.namespace}/${s.name}`}
          rowHref={(s) => detailHref('services', s.namespace, s.name)}
          filtered={query !== ''}
          isLoading={servicesQuery.isPending}
          error={loadError(servicesQuery)}
          actions={serviceActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}

      {tab === 'nodes' && (
        <DataTable
          columns={nodeColumns}
          data={filteredNodes ?? []}
          rowKey={(n) => n.name}
          rowHref={(n) => detailHref('nodes', undefined, n.name)}
          filtered={query !== ''}
          isLoading={nodesQuery.isPending}
          error={loadError(nodesQuery)}
          actions={nodeActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}

      {tab === 'events' && (
        <DataTable
          columns={eventColumns}
          data={(eventsQuery.data?.items ?? []).map((e, i) => ({ ...e, _key: String(i) }))}
          rowKey={(e) => e._key}
          filtered={eventsFiltered}
          isLoading={eventsQuery.isPending}
          error={loadError(eventsQuery)}
          state={tableState}
          onStateChange={setTableState}
        />
      )}
    </div>
  )
}
