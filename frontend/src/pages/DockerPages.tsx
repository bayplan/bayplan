import { lazy, Suspense, useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, Eye, Play, RefreshCw, ScrollText, Square, SquareTerminal, Trash2, X } from 'lucide-react'
import * as dockerApi from '../api/docker'
import { listClusters } from '../api/clusters'
import { LogsPanel } from '../components/LogsPanel'
import { SearchInput } from '../components/SearchInput'
import { Age, Alert, Badge, Button, Card, DataTable, Input, MenuItem, Modal, PageHeader, RowActions, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { formatBytes } from '../lib/format'
import { clusterConnection, connectionLabelKey, connectionTone, containerTone } from '../lib/status'
import { NotFound } from './NotFound'
import { apiErrorText } from '../lib/errors'

const TerminalPanel = lazy(() => import('../components/TerminalPanel').then((m) => ({ default: m.TerminalPanel })))

const POLL_INTERVAL_MS = 15000
const ICON = 'h-4 w-4 shrink-0'

/** Common context of the Docker pages: cluster, role and search filter in the URL. */
function useDockerPage() {
  const { id } = useParams<{ id: string }>()
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const isAdmin = cluster?.role === 'admin'
  const canWrite = isAdmin || cluster?.role === 'editor'
  const [filters, setFilters] = useUrlState(['q'] as const)
  const query = filters.q.trim().toLowerCase()
  const [actionError, setActionError] = useState<string | null>(null)
  const confirm = useConfirm()
  return { clusterId, validCluster, cluster, isAdmin, canWrite, query, setQuery: (q: string) => setFilters({ q }), actionError, setActionError, confirm }
}

function crumbs(t: (k: string) => string, cluster: { displayName: string } | undefined, clusterId: number, title: string) {
  return [{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: title }]
}

/** Summary of a Docker host: what `docker info` says about it. */
export function DockerOverview() {
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage ?? 'en'
  const { clusterId, validCluster, cluster } = useDockerPage()
  const infoQuery = useQuery({ queryKey: ['docker-info', clusterId], queryFn: () => dockerApi.getDockerInfo(clusterId), enabled: validCluster, refetchInterval: POLL_INTERVAL_MS, retry: false })
  if (!validCluster) return <NotFound />
  const connection = cluster ? clusterConnection(cluster) : 'unknown'
  const info = infoQuery.data
  const stat = (label: string, value: string | number) => (
    <div>
      <div className="text-lg font-semibold">{value}</div>
      <div className="text-xs text-fg-muted">{label}</div>
    </div>
  )
  return (
    <div>
      <PageHeader
        title={t('overview.title')}
        breadcrumb={crumbs(t, cluster, clusterId, t('overview.title'))}
        description={connection === 'ok' || connection === 'unknown' ? undefined : <Badge tone={connectionTone(connection)}>{t(connectionLabelKey(connection))}</Badge>}
      />
      {infoQuery.isError && <Alert tone="danger">{apiErrorText(infoQuery.error, t('docker.error'))}</Alert>}
      {info && (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card className="min-w-0 p-5">
            <h2 className="text-sm font-semibold">{t('docker.host')}</h2>
            <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
              <dt className="text-fg-muted">{t('docker.engine')}</dt>
              <dd className="font-mono">{info.podman ? 'Podman' : 'Docker'} {info.serverVersion}{info.swarm ? ' · Swarm' : ''}</dd>
              <dt className="text-fg-muted">{t('docker.os')}</dt>
              <dd>{info.operatingSystem} · {info.kernelVersion} · {info.architecture}</dd>
              <dt className="text-fg-muted">{t('docker.hostname')}</dt>
              <dd className="font-mono">{info.name}</dd>
              <dt className="text-fg-muted">{t('docker.resources')}</dt>
              <dd>{info.cpus} CPU · {formatBytes(info.memTotalBytes, locale)}</dd>
              <dt className="text-fg-muted">{t('docker.storageDriver')}</dt>
              <dd className="font-mono">{info.storageDriver}</dd>
            </dl>
          </Card>
          <Card className="min-w-0 p-5">
            <h2 className="text-sm font-semibold">{t('docker.containers')}</h2>
            <div className="mt-3 grid grid-cols-4 gap-2 text-center">
              {stat(t('docker.total'), info.containers)}
              {stat(t('docker.running'), info.containersRunning)}
              {stat(t('docker.stopped'), info.containersStopped)}
              {stat(t('docker.images'), info.images)}
            </div>
          </Card>
        </div>
      )}
    </div>
  )
}

export function DockerContainers() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { clusterId, validCluster, cluster, isAdmin, canWrite, query, setQuery, actionError, setActionError, confirm } = useDockerPage()
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [logsTarget, setLogsTarget] = useState<dockerApi.DockerContainer | null>(null)
  const [termTarget, setTermTarget] = useState<dockerApi.DockerContainer | null>(null)
  const [inspectTarget, setInspectTarget] = useState<dockerApi.DockerContainer | null>(null)
  const [inspectText, setInspectText] = useState('')

  const listQuery = useQuery({ queryKey: ['docker-containers', clusterId], queryFn: () => dockerApi.listContainers(clusterId), enabled: validCluster, refetchInterval: POLL_INTERVAL_MS, retry: false })
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['docker-containers', clusterId] })
  const onError = (err: unknown) => setActionError(apiErrorText(err, t('docker.error')))
  const actionMutation = useMutation({ mutationFn: ({ id, action }: { id: string; action: dockerApi.ContainerAction }) => dockerApi.containerAction(clusterId, id, action), onSuccess: invalidate, onError })
  const removeMutation = useMutation({ mutationFn: (id: string) => dockerApi.removeContainer(clusterId, id, { force: true }), onSuccess: invalidate, onError })

  const filtered = useMemo(() => {
    const items = listQuery.data ?? []
    if (!query) return items
    return items.filter((c) => c.name.toLowerCase().includes(query) || c.image.toLowerCase().includes(query) || (c.stack ?? '').toLowerCase().includes(query))
  }, [listQuery.data, query])

  async function openInspect(c: dockerApi.DockerContainer) {
    try {
      setInspectText(await dockerApi.inspectContainer(clusterId, c.id))
      setInspectTarget(c)
    } catch (err) {
      onError(err)
    }
  }

  const run = (c: dockerApi.DockerContainer, a: dockerApi.ContainerAction) => {
    setActionError(null)
    actionMutation.mutate({ id: c.id, action: a })
  }

  const columns: DataTableColumn<dockerApi.DockerContainer>[] = [
    { key: 'name', header: t('cluster.columns.name'), primary: true, cellClassName: 'font-mono', render: (c) => c.name, sortValue: (c) => c.name },
    { key: 'state', header: t('cluster.columns.status'), primary: true, render: (c) => <span title={c.status}><Badge tone={containerTone(c.state)}>{c.state}</Badge></span>, sortValue: (c) => c.state },
    { key: 'image', header: t('docker.image'), cellClassName: 'font-mono text-xs text-fg-muted', render: (c) => c.image, sortValue: (c) => c.image },
    { key: 'stack', header: t('docker.stack'), render: (c) => c.stack ?? '—', sortValue: (c) => c.stack ?? '' },
    {
      key: 'ports',
      header: t('docker.ports'),
      cellClassName: 'font-mono text-xs',
      // Docker lists the same binding twice (IPv4 and IPv6): it is deduplicated.
      render: (c) => [...new Set((c.ports ?? []).filter((p) => p.publicPort).map((p) => `${p.publicPort}→${p.privatePort}/${p.type}`))].join(', ') || '—',
    },
    { key: 'age', header: t('cluster.columns.age'), primary: true, cellClassName: 'whitespace-nowrap text-fg-muted', render: (c) => <Age createdAt={c.created} />, sortValue: (c) => -new Date(c.created).getTime() },
  ]

  // A Docker container has no detail page: logs and terminal are still
  // panels over the table, opened from the row menu.
  const rowActions = (c: dockerApi.DockerContainer) => (
    <RowActions>
      <MenuItem onSelect={() => setLogsTarget(c)}>
        <ScrollText className={ICON} aria-hidden="true" />
        {t('cluster.viewLogs')}
      </MenuItem>
      <MenuItem onSelect={() => void openInspect(c)}>
        <Eye className={ICON} aria-hidden="true" />
        {t('docker.inspect')}
      </MenuItem>
      {isAdmin && c.state === 'running' && (
        <MenuItem onSelect={() => setTermTarget(c)}>
          <SquareTerminal className={ICON} aria-hidden="true" />
          {t('terminal.open')}
        </MenuItem>
      )}
      {canWrite &&
        (c.state === 'running' ? (
          <>
            <MenuItem onSelect={() => run(c, 'restart')}>
              <RefreshCw className={ICON} aria-hidden="true" />
              {t('cluster.restart')}
            </MenuItem>
            <MenuItem onSelect={() => run(c, 'stop')}>
              <Square className={ICON} aria-hidden="true" />
              {t('docker.stop')}
            </MenuItem>
          </>
        ) : (
          <MenuItem onSelect={() => run(c, 'start')}>
            <Play className={ICON} aria-hidden="true" />
            {t('docker.start')}
          </MenuItem>
        ))}
      {isAdmin && (
        <MenuItem
          danger
          onSelect={async () => {
            if (!(await confirm({ title: t('cluster.delete'), description: t('cluster.confirmDelete', { name: c.name }), danger: true, typeToConfirm: c.name }))) return
            setActionError(null)
            removeMutation.mutate(c.id)
          }}
        >
          <Trash2 className={ICON} aria-hidden="true" />
          {t('cluster.delete')}
        </MenuItem>
      )}
    </RowActions>
  )

  if (!validCluster) return <NotFound />
  const title = t('docker.containers')
  return (
    <div>
      {logsTarget && (
        <LogsPanel clusterId={clusterId} namespace="" pod={logsTarget.name} container="" onClose={() => setLogsTarget(null)} openSocket={() => dockerApi.openContainerLogsSocket(clusterId, logsTarget.id)} subtitle={logsTarget.image} />
      )}
      {termTarget && (
        <Suspense fallback={null}>
          <TerminalPanel clusterId={clusterId} namespace="" pod={termTarget.name} containers={[]} onClose={() => setTermTarget(null)} openSocket={(_c, shell) => dockerApi.openContainerExecSocket(clusterId, termTarget.id, shell)} subtitle={termTarget.image} />
        </Suspense>
      )}
      {inspectTarget && (
        <Modal onClose={() => setInspectTarget(null)} size="lg" title={<span className="font-mono">{inspectTarget.name}</span>} bodyClassName="overflow-auto">
          <pre className="font-mono text-xs whitespace-pre">{inspectText}</pre>
        </Modal>
      )}
      <PageHeader title={title} breadcrumb={crumbs(t, cluster, clusterId, title)} />
      {actionError && (
        <Alert tone="danger" onClose={() => setActionError(null)} className="mb-3">
          {actionError}
        </Alert>
      )}
      <Toolbar>
        <SearchInput value={query} onChange={setQuery} placeholder={t('common.searchPlaceholder')} />
        {query !== '' && (
          <Button size="sm" variant="ghost" onClick={() => setQuery('')}>
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>
      <DataTable
        columns={columns}
        data={filtered}
        rowKey={(c) => c.id}
        filtered={query !== ''}
        isLoading={listQuery.isPending}
        error={listQuery.isError ? apiErrorText(listQuery.error, t('docker.error')) : undefined}
        actions={rowActions}
        state={tableState}
        onStateChange={setTableState}
      />
    </div>
  )
}

export function DockerImages() {
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage ?? 'en'
  const queryClient = useQueryClient()
  const { clusterId, validCluster, cluster, isAdmin, canWrite, query, setQuery, actionError, setActionError, confirm } = useDockerPage()
  const [tableState, setTableState] = useTableState({ sortKey: 'tags' })
  const [pullRef, setPullRef] = useState('')
  const listQuery = useQuery({ queryKey: ['docker-images', clusterId], queryFn: () => dockerApi.listImages(clusterId), enabled: validCluster, refetchInterval: 15000, retry: false })
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['docker-images', clusterId] })
  const onError = (err: unknown) => setActionError(apiErrorText(err, t('docker.error')))
  const pullMutation = useMutation({ mutationFn: (ref: string) => dockerApi.pullImage(clusterId, ref), onSuccess: () => { invalidate(); setPullRef('') }, onError })
  const removeMutation = useMutation({ mutationFn: (id: string) => dockerApi.removeImage(clusterId, id), onSuccess: invalidate, onError })

  const filtered = useMemo(() => {
    const items = listQuery.data ?? []
    if (!query) return items
    return items.filter((i) => i.tags.some((tg) => tg.toLowerCase().includes(query)) || i.id.startsWith(query))
  }, [listQuery.data, query])

  const columns: DataTableColumn<dockerApi.DockerImage>[] = [
    {
      key: 'tags',
      header: t('docker.image'),
      cellClassName: 'font-mono text-xs',
      render: (i) => (i.tags.length ? i.tags.map((tg) => <div key={tg}>{tg}</div>) : <span className="text-fg-muted">&lt;none&gt; {i.id.slice(0, 12)}</span>),
      sortValue: (i) => i.tags[0] ?? `~${i.id}`,
      primary: true,
    },
    { key: 'id', header: 'ID', cellClassName: 'font-mono text-xs text-fg-muted', render: (i) => i.id.slice(0, 12) },
    { key: 'size', header: t('docker.size'), primary: true, render: (i) => formatBytes(i.sizeBytes, locale), sortValue: (i) => i.sizeBytes },
    { key: 'inUse', header: t('docker.inUse'), primary: true, render: (i) => (i.inUse ? <Badge tone="success">{t('docker.used')}</Badge> : <Badge tone="neutral">{t('docker.unused')}</Badge>), sortValue: (i) => (i.inUse ? 0 : 1) },
    { key: 'age', header: t('cluster.columns.age'), cellClassName: 'whitespace-nowrap text-fg-muted', render: (i) => <Age createdAt={i.created} />, sortValue: (i) => -new Date(i.created).getTime() },
  ]

  const rowActions = (i: dockerApi.DockerImage) =>
    isAdmin ? (
      <RowActions>
        <MenuItem
          danger
          disabled={i.inUse}
          title={i.inUse ? t('docker.inUseHint') : undefined}
          onSelect={async () => {
            if (!(await confirm({ title: t('docker.confirmRemoveImage', { name: i.tags[0] ?? i.id.slice(0, 12) }), danger: true }))) return
            setActionError(null)
            removeMutation.mutate(i.id)
          }}
        >
          <Trash2 className={ICON} aria-hidden="true" />
          {t('cluster.delete')}
        </MenuItem>
      </RowActions>
    ) : null

  if (!validCluster) return <NotFound />
  const title = t('docker.images')
  return (
    <div>
      <PageHeader title={title} breadcrumb={crumbs(t, cluster, clusterId, title)} />
      {actionError && (
        <Alert tone="danger" onClose={() => setActionError(null)} className="mb-3">
          {actionError}
        </Alert>
      )}
      <Toolbar
        actions={
          canWrite ? (
            <form
              className="flex items-center gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                if (!pullRef.trim()) return
                setActionError(null)
                pullMutation.mutate(pullRef.trim())
              }}
            >
              <Input mono value={pullRef} onChange={(e) => setPullRef(e.target.value)} placeholder="nginx:1.27" className="w-auto" />
              <Button type="submit" size="sm" variant="primary" disabled={pullMutation.isPending}>
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
                {pullMutation.isPending ? t('docker.pulling') : t('docker.pull')}
              </Button>
            </form>
          ) : undefined
        }
      >
        <SearchInput value={query} onChange={setQuery} placeholder={t('common.searchPlaceholder')} />
        {query !== '' && (
          <Button size="sm" variant="ghost" onClick={() => setQuery('')}>
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>
      <DataTable
        columns={columns}
        data={filtered}
        rowKey={(i) => i.id}
        filtered={query !== ''}
        isLoading={listQuery.isPending}
        error={listQuery.isError ? apiErrorText(listQuery.error, t('docker.error')) : undefined}
        actions={rowActions}
        state={tableState}
        onStateChange={setTableState}
      />
    </div>
  )
}

export function DockerVolumes() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { clusterId, validCluster, cluster, isAdmin, query, setQuery, actionError, setActionError, confirm } = useDockerPage()
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const listQuery = useQuery({ queryKey: ['docker-volumes', clusterId], queryFn: () => dockerApi.listVolumes(clusterId), enabled: validCluster, refetchInterval: 15000, retry: false })
  const removeMutation = useMutation({
    mutationFn: (name: string) => dockerApi.removeVolume(clusterId, name),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['docker-volumes', clusterId] }),
    onError: (err) => setActionError(apiErrorText(err, t('docker.error'))),
  })
  const filtered = useMemo(() => {
    const items = listQuery.data ?? []
    return query ? items.filter((v) => v.name.toLowerCase().includes(query) || (v.stack ?? '').toLowerCase().includes(query)) : items
  }, [listQuery.data, query])
  const columns: DataTableColumn<dockerApi.DockerVolume>[] = [
    { key: 'name', header: t('cluster.columns.name'), primary: true, cellClassName: 'font-mono text-xs', render: (v) => v.name, sortValue: (v) => v.name },
    { key: 'driver', header: t('docker.driver'), primary: true, render: (v) => v.driver, sortValue: (v) => v.driver },
    { key: 'stack', header: t('docker.stack'), render: (v) => v.stack ?? '—', sortValue: (v) => v.stack ?? '' },
    { key: 'mount', header: t('docker.mountpoint'), cellClassName: 'font-mono text-xs text-fg-muted', render: (v) => v.mountpoint },
    // The daemon does not always report CreatedAt (old volumes, Podman).
    { key: 'age', header: t('cluster.columns.age'), primary: true, cellClassName: 'whitespace-nowrap text-fg-muted', render: (v) => (v.created ? <Age createdAt={v.created} /> : '—'), sortValue: (v) => (v.created ? -new Date(v.created).getTime() : 0) },
  ]
  const rowActions = (v: dockerApi.DockerVolume) =>
    isAdmin ? (
      <RowActions>
        <MenuItem
          danger
          onSelect={async () => {
            if (!(await confirm({ title: t('cluster.delete'), description: t('cluster.confirmDelete', { name: v.name }), danger: true, typeToConfirm: v.name }))) return
            setActionError(null)
            removeMutation.mutate(v.name)
          }}
        >
          <Trash2 className={ICON} aria-hidden="true" />
          {t('cluster.delete')}
        </MenuItem>
      </RowActions>
    ) : null
  if (!validCluster) return <NotFound />
  const title = t('docker.volumes')
  return (
    <div>
      <PageHeader title={title} breadcrumb={crumbs(t, cluster, clusterId, title)} />
      {actionError && (
        <Alert tone="danger" onClose={() => setActionError(null)} className="mb-3">
          {actionError}
        </Alert>
      )}
      <Toolbar>
        <SearchInput value={query} onChange={setQuery} placeholder={t('common.searchPlaceholder')} />
        {query !== '' && (
          <Button size="sm" variant="ghost" onClick={() => setQuery('')}>
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>
      <DataTable
        columns={columns}
        data={filtered}
        rowKey={(v) => v.name}
        filtered={query !== ''}
        isLoading={listQuery.isPending}
        error={listQuery.isError ? apiErrorText(listQuery.error, t('docker.error')) : undefined}
        actions={rowActions}
        state={tableState}
        onStateChange={setTableState}
      />
    </div>
  )
}

export function DockerNetworks() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { clusterId, validCluster, cluster, isAdmin, query, setQuery, actionError, setActionError, confirm } = useDockerPage()
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const listQuery = useQuery({ queryKey: ['docker-networks', clusterId], queryFn: () => dockerApi.listNetworks(clusterId), enabled: validCluster, refetchInterval: 15000, retry: false })
  const removeMutation = useMutation({
    mutationFn: (id: string) => dockerApi.removeNetwork(clusterId, id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['docker-networks', clusterId] }),
    onError: (err) => setActionError(apiErrorText(err, t('docker.error'))),
  })
  const filtered = useMemo(() => {
    const items = listQuery.data ?? []
    return query ? items.filter((n) => n.name.toLowerCase().includes(query) || (n.stack ?? '').toLowerCase().includes(query)) : items
  }, [listQuery.data, query])
  const builtin = new Set(['bridge', 'host', 'none', 'podman'])
  const columns: DataTableColumn<dockerApi.DockerNetwork>[] = [
    { key: 'name', header: t('cluster.columns.name'), primary: true, cellClassName: 'font-mono', render: (n) => n.name, sortValue: (n) => n.name },
    { key: 'driver', header: t('docker.driver'), primary: true, render: (n) => n.driver, sortValue: (n) => n.driver },
    { key: 'scope', header: t('docker.scope'), primary: true, render: (n) => <span className="flex gap-1"><Badge tone="neutral">{n.scope}</Badge>{n.internal && <Badge tone="warning">internal</Badge>}</span> },
    { key: 'subnets', header: t('docker.subnets'), cellClassName: 'font-mono text-xs', render: (n) => (n.subnets ?? []).join(', ') || '—' },
    { key: 'stack', header: t('docker.stack'), render: (n) => n.stack ?? '—', sortValue: (n) => n.stack ?? '' },
  ]
  const rowActions = (n: dockerApi.DockerNetwork) =>
    isAdmin && !builtin.has(n.name) ? (
      <RowActions>
        <MenuItem
          danger
          onSelect={async () => {
            if (!(await confirm({ title: t('cluster.delete'), description: t('cluster.confirmDelete', { name: n.name }), danger: true, typeToConfirm: n.name }))) return
            setActionError(null)
            removeMutation.mutate(n.id)
          }}
        >
          <Trash2 className={ICON} aria-hidden="true" />
          {t('cluster.delete')}
        </MenuItem>
      </RowActions>
    ) : null
  if (!validCluster) return <NotFound />
  const title = t('docker.networks')
  return (
    <div>
      <PageHeader title={title} breadcrumb={crumbs(t, cluster, clusterId, title)} />
      {actionError && (
        <Alert tone="danger" onClose={() => setActionError(null)} className="mb-3">
          {actionError}
        </Alert>
      )}
      <Toolbar>
        <SearchInput value={query} onChange={setQuery} placeholder={t('common.searchPlaceholder')} />
        {query !== '' && (
          <Button size="sm" variant="ghost" onClick={() => setQuery('')}>
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>
      <DataTable
        columns={columns}
        data={filtered}
        rowKey={(n) => n.id}
        filtered={query !== ''}
        isLoading={listQuery.isPending}
        error={listQuery.isError ? apiErrorText(listQuery.error, t('docker.error')) : undefined}
        actions={rowActions}
        state={tableState}
        onStateChange={setTableState}
      />
    </div>
  )
}
