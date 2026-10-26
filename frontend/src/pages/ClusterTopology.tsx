import { memo, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Background, Controls, Handle, MiniMap, Position, ReactFlow, ReactFlowProvider, useReactFlow, type Edge, type Node, type NodeProps } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { ExternalLink, Network, X } from 'lucide-react'
import { getTopology, type TopologyNode } from '../api/topology'
import { listClusters } from '../api/clusters'
import { SearchInput } from '../components/SearchInput'
import { ExplainButton } from '../components/ExplainButton'
import { Alert, Badge, Button, Card, Checkbox, DescriptionList, EmptyState, PageHeader, Spinner, Toolbar, type BadgeTone } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { apiErrorText } from '../lib/errors'
import { cn } from '../lib/cn'
import { NODE_HEIGHT, NODE_WIDTH, layoutTopology, relatedNodes } from '../lib/topologyLayout'

// Faster than the 15 s of the resource tables on purpose: this view exists to
// see something fall while it happens, and the response is served from the
// informer cache, so it costs the managed cluster nothing.
const REFRESH_MS = 5_000
// Above this many pods they start folded into their workloads: hundreds of
// replica cards hide the shape of the application instead of showing it.
const AUTO_FOLD_PODS = 200
const MAX_ATTENTION = 8

// Resource-detail route segment per kind; a kind without one (ReplicaSet) has
// no "open detail".
const DETAIL_TAB: Record<string, string> = {
  Pod: 'pods',
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
  Service: 'services',
  Ingress: 'ingresses',
  PersistentVolumeClaim: 'persistentvolumeclaims',
}

// Unknown statuses fall into amber, never into green (same rule as lib/status.ts).
function statusTone(status: string): BadgeTone {
  if (status === 'ok') return 'success'
  if (status === 'error') return 'danger'
  return 'warning'
}

// Literal classes: Tailwind v4 only generates what it finds written in the source.
const CARD_TONE: Record<BadgeTone, string> = {
  success: 'border-border-strong bg-surface',
  warning: 'border-warning bg-warning-soft',
  danger: 'border-danger bg-danger-soft',
  neutral: 'border-border-strong bg-surface',
}
const DOT_TONE: Record<BadgeTone, string> = {
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  neutral: 'bg-fg-muted',
}

interface ResourceData extends Record<string, unknown> {
  item: TopologyNode
  podCount?: number
  dimmed: boolean
  current: boolean
  reasonLabel?: string
}

const ResourceNode = memo(function ResourceNode({ data }: NodeProps<Node<ResourceData>>) {
  const { t } = useTranslation()
  const { item, podCount, dimmed, current, reasonLabel } = data
  const tone = statusTone(item.status)
  const meta = [reasonLabel ?? item.detail, podCount !== undefined ? t('topology.podsCount', { count: podCount }) : undefined, item.nodeName].filter(Boolean).join(' · ')
  return (
    <div
      className={cn('flex items-center gap-2 rounded-md border px-2.5 transition-opacity', CARD_TONE[tone], dimmed && 'opacity-25', current && 'ring-2 ring-fg')}
      style={{ width: NODE_WIDTH, height: NODE_HEIGHT }}
      title={`${item.kind} ${item.namespace}/${item.name}`}
    >
      <Handle type="target" position={Position.Left} isConnectable={false} className="opacity-0" />
      <span className="relative flex h-2.5 w-2.5 shrink-0">
        {tone === 'danger' && <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-danger opacity-60 motion-reduce:animate-none" />}
        <span className={cn('relative inline-flex h-2.5 w-2.5 rounded-full', DOT_TONE[tone])} />
      </span>
      <span className="min-w-0 flex-1 leading-tight">
        <span className="block truncate text-[10px] tracking-wide text-fg-muted uppercase">{item.kind}</span>
        <span className="block truncate text-xs font-semibold text-fg">{item.name}</span>
        {meta && <span className="block truncate text-[11px] text-fg-muted">{meta}</span>}
      </span>
      <Handle type="source" position={Position.Right} isConnectable={false} className="opacity-0" />
    </div>
  )
})

const NamespaceGroup = memo(function NamespaceGroup({ data }: NodeProps<Node<{ namespace: string }>>) {
  const { t } = useTranslation()
  return (
    <div className="h-full w-full rounded-lg border border-dashed border-border-strong bg-surface-hover/40">
      <span className="absolute top-3 left-4 text-[11px] tracking-wide text-fg-muted uppercase">
        <span className="font-semibold">{data.namespace}</span> · {t('topology.namespace')}
      </span>
    </div>
  )
})

const nodeTypes = { resource: ResourceNode, namespace: NamespaceGroup }

/** `Deployment/ns/api` → `Deployment api` (the namespace is already shown above). */
function ownerLabel(id: string): string {
  const [kind, , ...name] = id.split('/')
  return `${kind} ${name.join('/')}`
}

function useDarkTheme() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  useEffect(() => {
    const obs = new MutationObserver(() => setDark(document.documentElement.classList.contains('dark')))
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => obs.disconnect()
  }, [])
  return dark
}

/**
 * Cluster topology: Ingress → Service → workload → pods → volumes, grouped by
 * namespace, refreshed every few seconds from the informer cache. It draws
 * what the API server knows (owner, selector, route, volume), not observed
 * traffic, and the legend says so.
 */
export function ClusterTopology() {
  return (
    <ReactFlowProvider>
      <TopologyView />
    </ReactFlowProvider>
  )
}

function TopologyView() {
  const { id } = useParams<{ id: string }>()
  const clusterId = Number(id)
  const { t } = useTranslation()
  const navigate = useNavigate()
  const dark = useDarkTheme()
  const flow = useReactFlow()
  // Resolved colors, not var(--color-*): the minimap paints with the SVG `fill`
  // attribute, where CSS variables do not resolve, and React Flow's own
  // stylesheet beats a utility class. Re-read when the theme changes.
  const minimapColors = useMemo<Record<BadgeTone, string>>(() => {
    const css = getComputedStyle(document.documentElement)
    const color = (name: string) => css.getPropertyValue(name).trim()
    return { success: color('--color-fg-muted'), warning: color('--color-warning'), danger: color('--color-danger'), neutral: color('--color-fg-subtle') }
    // `dark` is not read but is the trigger: the variables change with the class.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dark])
  // The selection lives in the URL like the filters: a link can point at "this
  // pod and everything around it".
  const [filters, setFilters] = useUrlState(['ns', 'q', 'pods', 'sel'] as const)
  const namespaces = splitNamespaces(filters.ns)
  const query = filters.q.trim().toLowerCase()

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const topologyQuery = useQuery({
    queryKey: ['topology', clusterId, filters.ns],
    queryFn: () => getTopology(clusterId, namespaces),
    enabled: Number.isInteger(clusterId),
    refetchInterval: REFRESH_MS,
    retry: false,
  })
  const data = topologyQuery.data

  const podTotal = useMemo(() => data?.nodes.filter((n) => n.kind === 'Pod').length ?? 0, [data])
  const showPods = filters.pods === '' ? podTotal <= AUTO_FOLD_PODS : filters.pods === '1'

  const layout = useMemo(() => (data ? layoutTopology(data.nodes, data.edges, showPods) : undefined), [data, showPods])
  const byId = useMemo(() => new Map((data?.nodes ?? []).map((n) => [n.id, n])), [data])
  const selected = filters.sel && byId.has(filters.sel) ? byId.get(filters.sel) : undefined

  // Highlight: the selection and its whole chain, or the search matches.
  const highlighted = useMemo(() => {
    if (!layout) return undefined
    if (selected) return relatedNodes([selected.id], layout.edges)
    if (query) {
      const matches = layout.groups.flatMap((g) => g.nodes).filter((n) => n.node.name.toLowerCase().includes(query))
      return new Set(matches.map((n) => n.node.id))
    }
    return undefined
  }, [layout, selected, query])

  const reasonLabel = (reason?: string) => (reason ? t(`topology.reason.${reason}`, { defaultValue: t(`overview.reason.${reason}`, { defaultValue: reason }) }) : undefined)

  const nodes = useMemo<Node[]>(() => {
    if (!layout) return []
    const out: Node[] = []
    for (const g of layout.groups) {
      // Fixed sizes on every node: the flow is controlled without onNodesChange,
      // so measured dimensions never reach it and the minimap drew nothing.
      out.push({ id: `ns:${g.namespace}`, type: 'namespace', position: { x: g.x, y: g.y }, data: { namespace: g.namespace }, width: g.width, height: g.height, selectable: false, focusable: false })
      for (const n of g.nodes) {
        out.push({
          id: n.node.id,
          type: 'resource',
          parentId: `ns:${g.namespace}`,
          position: { x: n.x, y: n.y },
          width: NODE_WIDTH,
          height: NODE_HEIGHT,
          data: { item: n.node, podCount: n.podCount, dimmed: highlighted ? !highlighted.has(n.node.id) : false, current: n.node.id === selected?.id, reasonLabel: reasonLabel(n.node.reason) } satisfies ResourceData,
          ariaLabel: `${n.node.kind} ${n.node.namespace}/${n.node.name}`,
        })
      }
    }
    return out
    // reasonLabel only depends on t.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [layout, highlighted, selected, t])

  const edges = useMemo<Edge[]>(() => {
    if (!layout) return []
    return layout.edges.map((e) => {
      const lit = !highlighted || (highlighted.has(e.source) && highlighted.has(e.target))
      const failing = byId.get(e.target)?.status === 'error'
      return {
        id: `${e.source}>${e.target}`,
        source: e.source,
        target: e.target,
        animated: failing,
        selectable: false,
        focusable: false,
        style: {
          stroke: failing ? 'var(--color-danger)' : highlighted && lit ? 'var(--color-fg)' : 'var(--color-border-strong)',
          strokeWidth: highlighted && lit ? 2 : 1.5,
          opacity: lit ? 1 : 0.15,
        },
      }
    })
  }, [layout, highlighted, byId])

  // Fit when what is drawn changes shape (namespaces, folded pods), not on
  // every refresh: re-fitting every 5 s would undo the user's zoom.
  const shapeKey = `${filters.ns}|${showPods}|${layout?.groups.length ?? 0}`
  useEffect(() => {
    if (!layout) return
    const frame = requestAnimationFrame(() => void flow.fitView({ padding: 0.1, duration: 300 }))
    return () => cancelAnimationFrame(frame)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shapeKey])

  const focus = (nodeId: string) => {
    setFilters({ sel: nodeId })
    void flow.fitView({ nodes: [{ id: nodeId }], padding: 1.5, duration: 400, maxZoom: 1.2 })
  }

  const problems = useMemo(() => {
    const items = (data?.nodes ?? []).filter((n) => n.status !== 'ok')
    // Errors first, then by kind so a failing Service shows before its pods.
    return items.sort((a, b) => Number(b.status === 'error') - Number(a.status === 'error') || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name))
  }, [data])

  const unavailable = data?.unavailable ? Object.keys(data.unavailable) : []
  const detailTab = selected ? DETAIL_TAB[selected.kind] : undefined

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title={t('topology.title')}
        description={t('topology.hint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('topology.title') }]}
        actions={
          <span className="flex items-center gap-2 text-xs text-fg-muted">
            <span className={cn('h-2 w-2 rounded-full', topologyQuery.isError ? 'bg-danger' : 'animate-pulse bg-success motion-reduce:animate-none')} />
            {topologyQuery.isError ? t('topology.stale') : t('topology.live')}
          </span>
        }
      />
      <Toolbar>
        <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v, sel: '' })} placeholder={t('common.searchPlaceholder')} />
        <Checkbox label={t('topology.showPods')} checked={showPods} onChange={(e) => setFilters({ pods: e.target.checked ? '1' : '0' })} />
        {data && !data.tooLarge && <span className="text-xs text-fg-muted">{t('topology.counts', { count: data.nodes.length })}</span>}
      </Toolbar>

      {topologyQuery.isError && data && (
        <Alert tone="warning" className="mb-3">
          {apiErrorText(topologyQuery.error, t('topology.error'))}
        </Alert>
      )}
      {unavailable.length > 0 && (
        <Alert tone="info" className="mb-3">
          {t('topology.unavailable', { kinds: unavailable.join(', ') })}
        </Alert>
      )}

      <div className="flex min-h-[480px] flex-1 flex-col gap-3 lg:flex-row">
        <Card className="relative min-h-[420px] min-w-0 flex-1 overflow-hidden p-0">
          {topologyQuery.isPending ? (
            <Spinner className="p-6" />
          ) : topologyQuery.isError && !data ? (
            <Alert tone="danger" className="m-4">
              {apiErrorText(topologyQuery.error, t('topology.error'))}
            </Alert>
          ) : data?.tooLarge ? (
            <EmptyState icon={Network} title={t('topology.tooLarge', { count: data.total })} />
          ) : nodes.length === 0 ? (
            <EmptyState icon={Network} title={t('topology.empty')} />
          ) : (
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              colorMode={dark ? 'dark' : 'light'}
              nodesDraggable={false}
              nodesConnectable={false}
              onlyRenderVisibleElements
              minZoom={0.05}
              maxZoom={2}
              fitView
              onNodeClick={(_, node) => {
                if (node.type !== 'resource') return
                setFilters({ sel: node.id === selected?.id ? '' : node.id })
              }}
              onNodeDoubleClick={(_, node) => {
                const item = byId.get(node.id)
                const tab = item ? DETAIL_TAB[item.kind] : undefined
                if (item && tab) navigate(`/clusters/${clusterId}/${tab}/${item.namespace}/${item.name}`)
              }}
              onPaneClick={() => selected && setFilters({ sel: '' })}
            >
              <Background gap={24} />
              <Controls showInteractive={false} />
              <MiniMap pannable zoomable nodeColor={(n) => (n.type === 'namespace' ? 'transparent' : minimapColors[statusTone((n.data as ResourceData).item.status)])} />
            </ReactFlow>
          )}
        </Card>

        <Card className="flex shrink-0 flex-col gap-3 p-4 lg:w-80">
          {selected ? (
            <>
              <div className="flex items-start gap-2">
                <div className="min-w-0 flex-1">
                  <p className="text-[11px] tracking-wide text-fg-muted uppercase">
                    {selected.kind} · {selected.namespace}
                  </p>
                  <p className="font-semibold break-all">{selected.name}</p>
                </div>
                <Button size="sm" variant="ghost" aria-label={t('topology.clearSelection')} onClick={() => setFilters({ sel: '' })}>
                  <X className="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              </div>
              <Badge tone={statusTone(selected.status)} className="self-start">
                {reasonLabel(selected.reason) ?? t(`topology.status.${selected.status}`, { defaultValue: selected.status })}
              </Badge>
              <DescriptionList
                columns={1}
                items={[
                  ...(selected.detail ? [{ label: t('topology.fields.detail'), value: selected.detail }] : []),
                  ...(selected.nodeName ? [{ label: t('topology.fields.node'), value: selected.nodeName, mono: true }] : []),
                  ...(selected.owner ? [{ label: t('topology.fields.owner'), value: ownerLabel(selected.owner) }] : []),
                  ...(selected.config?.length ? [{ label: t('topology.fields.config'), value: selected.config.join(', '), mono: true }] : []),
                ]}
              />
              <p className="text-xs text-fg-muted">{t('topology.related', { count: (highlighted?.size ?? 1) - 1 })}</p>
              <div className="flex flex-wrap gap-2">
                {detailTab && (
                  <Button variant="primary" size="sm" onClick={() => navigate(`/clusters/${clusterId}/${detailTab}/${selected.namespace}/${selected.name}`)}>
                    <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                    {t('topology.openDetail')}
                  </Button>
                )}
                {detailTab && <ExplainButton clusterId={clusterId} role={cluster?.role} kind={detailTab} namespace={selected.namespace} name={selected.name} />}
              </div>
            </>
          ) : (
            <>
              <p className="text-sm font-semibold">{t('topology.attention')}</p>
              <p className="text-xs text-fg-muted">{t('topology.clickHint')}</p>
              {problems.length === 0 ? (
                <p className="text-sm text-fg-muted">{t('topology.allGood')}</p>
              ) : (
                <ul className="flex flex-col gap-1.5">
                  {problems.slice(0, MAX_ATTENTION).map((p) => (
                    <li key={p.id}>
                      <button
                        type="button"
                        onClick={() => focus(p.id)}
                        className={cn('flex w-full flex-col rounded-md border px-2.5 py-1.5 text-left hover:bg-surface-hover', p.status === 'error' ? 'border-danger' : 'border-warning')}
                      >
                        <span className="truncate text-xs font-semibold">{p.name}</span>
                        <span className="truncate text-[11px] text-fg-muted">
                          {p.kind} · {p.namespace} · {reasonLabel(p.reason)}
                        </span>
                      </button>
                    </li>
                  ))}
                  {problems.length > MAX_ATTENTION && <li className="text-xs text-fg-muted">{t('topology.attentionMore', { count: problems.length - MAX_ATTENTION })}</li>}
                </ul>
              )}
            </>
          )}
          <div className="mt-auto border-t border-border pt-3 text-xs text-fg-muted">
            <p className="flex items-center gap-2">
              <svg width="24" height="6" aria-hidden="true">
                <path d="M1 3h22" stroke="var(--color-border-strong)" strokeWidth="1.5" />
              </svg>
              {t('topology.legendConfigured')}
            </p>
            <p className="mt-1 flex items-center gap-2">
              <svg width="24" height="6" aria-hidden="true">
                <path d="M1 3h22" stroke="var(--color-danger)" strokeWidth="1.5" strokeDasharray="5 4" />
              </svg>
              {t('topology.legendFailing')}
            </p>
          </div>
        </Card>
      </div>
    </div>
  )
}
