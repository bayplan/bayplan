import { lazy, Suspense, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, RefreshCw, RotateCcw, Trash2, Ban, CircleCheck, Wind } from 'lucide-react'
import * as resources from '../api/resources'
import { listClusters } from '../api/clusters'
import { isExplainableKind } from '../api/ai'
import { ExplainButton } from '../components/ExplainButton'
import { YamlEditor } from '../components/YamlEditor'
import { LogsPanel } from '../components/LogsPanel'
import {
  Age,
  Alert,
  Badge,
  Button,
  Card,
  DescriptionList,
  EmptyState,
  Input,
  PageHeader,
  Select,
  Spinner,
  Tabs,
  useConfirm,
  type BadgeTone,
  type DescriptionItem,
} from '../components/ui'
import { deploymentTone, nodeTone, podTone } from '../lib/status'
import { apiErrorText } from '../lib/errors'
import { NotFound } from './NotFound'
import { formatDateTime } from '../lib/dates'

// xterm is only downloaded when the Terminal tab is opened.
const TerminalPanel = lazy(() => import('../components/TerminalPanel').then((m) => ({ default: m.TerminalPanel })))

type View = 'summary' | 'yaml' | 'events' | 'logs' | 'terminal'

// Kinds with a detail page: the ones the backend serves as YAML. The list is
// used to validate the URL segment, which is user input.
const DETAIL_KINDS: ReadonlySet<string> = new Set<string>([
  'pods',
  'deployments',
  'statefulsets',
  'daemonsets',
  'services',
  'nodes',
  ...resources.OPTIONAL_KINDS,
])

// Name and namespace come from the URL (user input) and are interpolated into
// API paths: only a Kubernetes object name is accepted (DNS-1123 label /
// subdomain), so a "../" or a "?" can never end up composing a path other
// than the intended one.
const K8S_NAME = /^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$/

const RESTARTABLE: ReadonlySet<string> = new Set(['deployments', 'statefulsets', 'daemonsets'])
const SCALABLE: ReadonlySet<string> = new Set(['deployments', 'statefulsets'])

type Row = Record<string, unknown> & { name: string; namespace?: string }

// A single load function per kind: the detail is built from the summary the
// listing already serves (from the informer cache), not from a new call to the
// cluster, and React Query shares the cache with the table one comes from.
async function listForKind(clusterId: number, kind: string, namespace: string): Promise<Row[]> {
  const nss = namespace ? [namespace] : []
  // The summaries are interfaces without an index signature; for the generic
  // detail they are treated as a plain object (they are JSON from the backend).
  const asRows = (p: Promise<object[]>) => p as Promise<Row[]>
  switch (kind) {
    case 'pods':
      return asRows(resources.listPods(clusterId, nss))
    case 'deployments':
      return asRows(resources.listDeployments(clusterId, nss))
    case 'statefulsets':
      return asRows(resources.listStatefulSets(clusterId, nss))
    case 'daemonsets':
      return asRows(resources.listDaemonSets(clusterId, nss))
    case 'services':
      return asRows(resources.listServices(clusterId, nss))
    case 'nodes':
      return asRows(resources.listNodes(clusterId))
    default:
      return resources.listKind<Row>(clusterId, kind as resources.OptionalKind, nss)
  }
}

function statusTone(kind: string, row: Row): BadgeTone | undefined {
  if (kind === 'pods') return podTone(String(row.status))
  if (kind === 'nodes') return nodeTone(String(row.status))
  if (kind === 'deployments' || kind === 'statefulsets') {
    return deploymentTone({ replicas: Number(row.replicas), readyReplicas: Number(row.readyReplicas) })
  }
  return undefined
}

/**
 * Resource detail page: `/clusters/:id/:kind/:namespace/:name/:view?`
 * (`-` as namespace for cluster-scoped kinds). It replaces the YAML/logs/terminal
 * drawers that used to open over the table: one URL per resource can be
 * shared, opened in another tab and navigated back with the browser. Write
 * actions live in the header; what each role can do is
 * decided by the backend, here the buttons are merely hidden.
 */
export function ResourceDetail() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const params = useParams<{ id: string; tab: string; namespace: string; name: string; view?: string }>()
  const clusterId = Number(params.id)
  const kind = params.tab ?? ''
  const namespace = params.namespace === '-' ? '' : (params.namespace ?? '')
  const name = params.name ?? ''
  const view: View = (['summary', 'yaml', 'events', 'logs', 'terminal'] as View[]).includes(params.view as View)
    ? (params.view as View)
    : 'summary'
  const valid =
    Number.isInteger(clusterId) && clusterId > 0 && DETAIL_KINDS.has(kind) && K8S_NAME.test(name) && (namespace === '' || K8S_NAME.test(namespace))

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const role = cluster?.role ?? ''
  const canWrite = role === 'admin' || role === 'editor'
  const isAdmin = role === 'admin'

  const listQuery = useQuery({
    queryKey: [kind, clusterId, namespace ? [namespace] : []],
    queryFn: () => listForKind(clusterId, kind, namespace),
    enabled: valid,
    refetchInterval: 15000,
  })
  const row = useMemo(
    () => listQuery.data?.find((r) => r.name === name && (!namespace || r.namespace === namespace)),
    [listQuery.data, name, namespace],
  )

  const [replicasDraft, setReplicasDraft] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [actionNotice, setActionNotice] = useState<string | null>(null)

  const base = `/clusters/${clusterId}/${kind}/${params.namespace}/${name}`
  const listHref = `/clusters/${clusterId}/${kind}`

  function invalidate() {
    void queryClient.invalidateQueries({ queryKey: [kind, clusterId] })
  }

  const action = useMutation({
    mutationFn: async (fn: () => Promise<unknown>) => fn(),
    onSuccess: () => {
      setActionError(null)
      invalidate()
    },
    onError: (err) => setActionError(apiErrorText(err, t('common.error'))),
  })

  async function handleRestart() {
    if (!(await confirm({ title: t('cluster.restart'), description: t('cluster.confirmRestart', { name }) }))) return
    action.mutate(() => resources.restartWorkload(clusterId, kind as resources.RestartableKind, namespace, name))
  }
  async function handleRollback() {
    if (!(await confirm({ title: t('cluster.rollback'), description: t('cluster.confirmRollback', { name }) }))) return
    action.mutate(() => resources.rollbackDeployment(clusterId, namespace, name))
  }
  async function handleDelete() {
    if (!(await confirm({ title: t('cluster.delete'), description: t('cluster.confirmDelete', { name }), danger: true, typeToConfirm: name }))) return
    action.mutate(async () => {
      await resources.deleteResource(clusterId, kind as resources.YamlKind, namespace || undefined, name)
      navigate(listHref)
    })
  }
  async function handleDrain() {
    if (!(await confirm({ title: t('cluster.drain'), description: t('cluster.confirmDrain', { name }), danger: true, typeToConfirm: name }))) return
    action.mutate(async () => {
      const res = await resources.drainNode(clusterId, name)
      setActionNotice(t('cluster.drainResult', { evicted: res.evicted.length, skipped: res.skipped.length, failed: res.failed.length }))
    })
  }
  function handleScale() {
    const n = Number(replicasDraft)
    if (!Number.isInteger(n) || n < 0 || n > 100) return
    action.mutate(async () => {
      await resources.scaleWorkload(clusterId, kind as resources.ScalableKind, namespace, name, n)
      setReplicasDraft(null)
    })
  }

  if (!valid) return <NotFound />

  const containers = (row?.containers as string[] | undefined) ?? []
  const tabs: { key: View; label: string }[] = [
    { key: 'summary', label: t('detail.tabs.summary') },
    { key: 'yaml', label: t('detail.tabs.yaml') },
  ]
  if (kind !== 'nodes' && kind !== 'persistentvolumes' && kind !== 'storageclasses') tabs.push({ key: 'events', label: t('detail.tabs.events') })
  if (kind === 'pods') {
    tabs.push({ key: 'logs', label: t('detail.tabs.logs') })
    if (isAdmin) tabs.push({ key: 'terminal', label: t('detail.tabs.terminal') })
  }

  const tone = row ? statusTone(kind, row) : undefined
  const unschedulable = kind === 'nodes' && row?.unschedulable === true

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        breadcrumb={[
          { label: t('nav.dashboard'), to: '/' },
          { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` },
          { label: t(`cluster.tabs.${kind}`, { defaultValue: kind }), to: listHref },
          { label: name },
        ]}
        title={<span className="font-mono">{name}</span>}
        description={
          <span className="flex flex-wrap items-center gap-2">
            {namespace && <span className="font-mono text-xs">{namespace}</span>}
            {tone && row && <Badge tone={tone}>{String(row.status ?? `${row.readyReplicas}/${row.replicas}`)}</Badge>}
            {unschedulable && <Badge tone="warning">{t('cluster.cordoned')}</Badge>}
          </span>
        }
        actions={
          <>
            {isExplainableKind(kind) && <ExplainButton clusterId={clusterId} role={role} kind={kind} namespace={namespace || undefined} name={name} />}
            {canWrite && view !== 'yaml' && (
              <Button size="sm" onClick={() => navigate(`${base}/yaml`)}>
                <Pencil aria-hidden="true" />
                {t('detail.edit')}
              </Button>
            )}
            {canWrite && RESTARTABLE.has(kind) && (
              <Button size="sm" onClick={() => void handleRestart()} loading={action.isPending}>
                <RefreshCw aria-hidden="true" />
                {t('cluster.restart')}
              </Button>
            )}
            {canWrite && SCALABLE.has(kind) && row && (
              replicasDraft === null ? (
                <Button size="sm" onClick={() => setReplicasDraft(String(row.replicas ?? 0))}>
                  {t('cluster.scale')}
                </Button>
              ) : (
                <form
                  className="flex items-center gap-1"
                  onSubmit={(e) => {
                    e.preventDefault()
                    handleScale()
                  }}
                >
                  <Input
                    type="number"
                    min={0}
                    max={100}
                    value={replicasDraft}
                    onChange={(e) => setReplicasDraft(e.target.value)}
                    className="h-8 w-20"
                    autoFocus
                    aria-label={t('cluster.scale')}
                  />
                  <Button size="sm" variant="primary" type="submit" loading={action.isPending}>
                    {t('cluster.scale')}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setReplicasDraft(null)}>
                    {t('confirm.cancel')}
                  </Button>
                </form>
              )
            )}
            {canWrite && kind === 'deployments' && (
              <Button size="sm" onClick={() => void handleRollback()}>
                <RotateCcw aria-hidden="true" />
                {t('cluster.rollback')}
              </Button>
            )}
            {canWrite && kind === 'nodes' && (
              <Button size="sm" onClick={() => action.mutate(() => resources.cordonNode(clusterId, name, !unschedulable))}>
                {unschedulable ? <CircleCheck aria-hidden="true" /> : <Ban aria-hidden="true" />}
                {unschedulable ? t('cluster.uncordon') : t('cluster.cordon')}
              </Button>
            )}
            {isAdmin && kind === 'nodes' && (
              <Button size="sm" variant="danger" onClick={() => void handleDrain()}>
                <Wind aria-hidden="true" />
                {t('cluster.drain')}
              </Button>
            )}
            {isAdmin && kind !== 'nodes' && (
              <Button size="sm" variant="danger" onClick={() => void handleDelete()}>
                <Trash2 aria-hidden="true" />
                {t('cluster.delete')}
              </Button>
            )}
          </>
        }
      />

      {actionError && (
        <Alert tone="danger" className="mb-3" onClose={() => setActionError(null)}>
          {actionError}
        </Alert>
      )}
      {actionNotice && (
        <Alert tone="info" className="mb-3" onClose={() => setActionNotice(null)}>
          {actionNotice}
        </Alert>
      )}

      <Tabs items={tabs} active={view} onChange={(v) => navigate(v === 'summary' ? base : `${base}/${v}`)} />

      <div className="mt-4 flex min-h-0 flex-1 flex-col">
        {listQuery.isError && <Alert tone="danger">{apiErrorText(listQuery.error, t('common.error'))}</Alert>}
        {!listQuery.isError && listQuery.isPending && <Spinner />}
        {!listQuery.isError && !listQuery.isPending && !row && (
          <EmptyState
            title={t('detail.notFound')}
            description={t('detail.notFoundHint')}
            action={
              <Button size="sm" onClick={() => navigate(listHref)}>
                {t('detail.backToList')}
              </Button>
            }
          />
        )}
        {row && view === 'summary' && <SummaryTab kind={kind} row={row} />}
        {row && view === 'yaml' && (
          <YamlEditor
            inline
            clusterId={clusterId}
            kind={kind as resources.YamlKind}
            namespace={namespace}
            name={name}
            canWrite={canWrite}
            onClose={() => {}}
            onApplied={() => invalidate()}
          />
        )}
        {row && view === 'events' && <EventsTab clusterId={clusterId} namespace={namespace} name={name} />}
        {row && view === 'logs' && kind === 'pods' && <LogsTab clusterId={clusterId} namespace={namespace} pod={name} containers={containers} />}
        {row && view === 'terminal' && kind === 'pods' && isAdmin && (
          <Suspense fallback={<Spinner />}>
            <TerminalPanel inline clusterId={clusterId} namespace={namespace} pod={name} containers={containers} onClose={() => navigate(base)} />
          </Suspense>
        )}
      </div>

    </div>
  )
}

// Summary keys not listed as a field: they are already in the header.
const HIDDEN_KEYS = new Set(['name', 'namespace'])

function SummaryTab({ kind, row }: { kind: string; row: Row }) {
  const { t, i18n } = useTranslation()
  const items: DescriptionItem[] = Object.entries(row)
    .filter(([k, v]) => !HIDDEN_KEYS.has(k) && v !== undefined && v !== null && v !== '')
    .map(([k, v]) => ({
      // The column label if the table has one; otherwise the key as is:
      // better a field with a technical name than a field that is not shown.
      label: t(`cluster.columns.${k}`, { defaultValue: t(`resources.columns.${k}`, { defaultValue: k }) }),
      value: formatValue(k, v, t, i18n.resolvedLanguage),
      mono: typeof v === 'string' && (k.endsWith('IP') || k === 'node' || k === 'version' || k === 'image'),
    }))
  return (
    <Card className="p-5">
      <DescriptionList items={items} columns={kind === 'pods' || kind === 'nodes' ? 3 : 2} />
    </Card>
  )
}

function formatValue(key: string, value: unknown, t: (k: string, o?: Record<string, unknown>) => string, locale: string | undefined) {
  if (key === 'createdAt' && typeof value === 'string') {
    return (
      <>
        <Age createdAt={value} /> <span className="text-xs text-fg-muted">({formatDateTime(value, locale)})</span>
      </>
    )
  }
  if (typeof value === 'boolean') return value ? t('detail.yes') : t('detail.no')
  if (Array.isArray(value)) return value.length === 0 ? '—' : value.map(String).join(', ')
  if (typeof value === 'object') return <code className="font-mono text-xs">{JSON.stringify(value)}</code>
  return String(value)
}

function EventsTab({ clusterId, namespace, name }: { clusterId: number; namespace: string; name: string }) {
  const { t } = useTranslation()
  const [type, setType] = useState('')
  const query = useQuery({
    queryKey: ['events', clusterId, namespace ? [namespace] : [], { type }],
    queryFn: () => resources.listEvents(clusterId, namespace ? [namespace] : [], type ? { type } : {}),
    refetchInterval: 15000,
  })
  // The backend returns "Kind/name"; here we want everything pointing at this
  // object. Filtered client-side: the events are already cached per namespace.
  const items = (query.data?.items ?? []).filter((e) => e.involvedObject.endsWith(`/${name}`) || e.involvedObject === name)
  if (query.isError) return <Alert tone="danger">{apiErrorText(query.error, t('common.error'))}</Alert>
  return (
    <div className="space-y-3">
      <Select value={type} onChange={(e) => setType(e.target.value)} className="w-auto" aria-label={t('cluster.columns.type')}>
        <option value="">{t('cluster.events.allTypes')}</option>
        <option value="Normal">{t('cluster.events.typeInfo')}</option>
        <option value="Warning">{t('cluster.events.typeWarning')}</option>
      </Select>
      {query.isPending ? (
        <Spinner />
      ) : items.length === 0 ? (
        <EmptyState title={t('detail.noEvents')} />
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border bg-surface">
          {items.map((e, i) => (
            <li key={`${e.lastSeen}-${i}`} className="flex flex-wrap items-start gap-x-3 gap-y-1 px-4 py-2.5 text-sm">
              <Badge tone={e.type === 'Warning' ? 'warning' : 'neutral'}>{e.reason}</Badge>
              <span className="min-w-0 flex-1 break-words">{e.message}</span>
              <span className="shrink-0 text-xs text-fg-muted">
                {e.count > 1 && `×${e.count} · `}
                <Age createdAt={e.lastSeen} />
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function LogsTab({ clusterId, namespace, pod, containers }: { clusterId: number; namespace: string; pod: string; containers: string[] }) {
  const { t } = useTranslation()
  const [container, setContainer] = useState(containers[0] ?? '')
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      {containers.length > 1 && (
        <Select value={container} onChange={(e) => setContainer(e.target.value)} className="w-auto" aria-label={t('logs.defaultContainer')}>
          {containers.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </Select>
      )}
      <div className="min-h-0 flex-1">
        <LogsPanel inline clusterId={clusterId} namespace={namespace} pod={pod} container={container} onClose={() => {}} />
      </div>
    </div>
  )
}
