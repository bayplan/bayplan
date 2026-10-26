import { useMemo, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, Play, RotateCcw, Trash2, X } from 'lucide-react'
import * as backupsApi from '../api/backups'
import * as resources from '../api/resources'
import { listClusters } from '../api/clusters'
import { useAuth } from '../hooks/useAuth'
import { SearchInput } from '../components/SearchInput'
import { Age, Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, Modal, PageHeader, RowActions, Select, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { formatBytes } from '../lib/format'
import { NotFound } from './NotFound'
import { apiErrorText } from '../lib/errors'
import { formatDateTime } from '../lib/dates'

/**
 * Cluster configuration backups: schedules (what, where to, how
 * often), runs (download, restore) and the honest warning that
 * this does not back up volumes.
 */
export function ClusterBackups() {
  const { id } = useParams<{ id: string }>()
  const { t, i18n } = useTranslation()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0
  const locale = i18n.resolvedLanguage ?? 'en'

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const isAdmin = cluster?.role === 'admin'
  const schedulesQuery = useQuery({ queryKey: ['backup-schedules', clusterId], queryFn: () => backupsApi.listSchedules(clusterId), enabled: validCluster && isAdmin })
  const runsQuery = useQuery({ queryKey: ['backup-runs', clusterId], queryFn: () => backupsApi.listRuns(clusterId), enabled: validCluster && isAdmin, refetchInterval: 60_000 })
  const targetsQuery = useQuery({ queryKey: ['backup-targets'], queryFn: backupsApi.listTargets, enabled: isAdmin })
  const namespacesQuery = useQuery({ queryKey: ['namespaces', clusterId], queryFn: () => resources.listNamespaces(clusterId), enabled: validCluster && isAdmin })
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['backup-schedules', clusterId] })
    void queryClient.invalidateQueries({ queryKey: ['backup-runs', clusterId] })
    void queryClient.invalidateQueries({ queryKey: ['backup-targets'] })
  }
  const [schedState, setSchedState] = useTableState({ sortKey: 'name' })
  const [form, setForm] = useState({ name: '', targetId: '', namespaces: '', includeSecrets: false, intervalHours: 24, retention: 14 })
  const [error, setError] = useState<string | null>(null)
  const [restoreRun, setRestoreRun] = useState<backupsApi.BackupRun | null>(null)

  const createMutation = useMutation({
    mutationFn: () =>
      backupsApi.createSchedule(clusterId, {
        name: form.name.trim(),
        targetId: Number(form.targetId),
        namespaces: form.namespaces.split(/[,\s]+/).map((x) => x.trim()).filter(Boolean),
        includeSecrets: form.includeSecrets,
        intervalHours: Number(form.intervalHours),
        retention: Number(form.retention),
      }),
    onSuccess: () => {
      invalidate()
      setForm((f) => ({ ...f, name: '', namespaces: '' }))
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('backups.error'))),
  })
  const runMutation = useMutation({
    mutationFn: (sid: number) => backupsApi.runSchedule(clusterId, sid),
    onSuccess: () => {
      invalidate()
      setError(null)
    },
    onError: (err) => {
      invalidate()
      setError(apiErrorText(err, t('backups.error')))
    },
  })
  const toggleMutation = useMutation({
    mutationFn: ({ sid, enabled }: { sid: number; enabled: boolean }) => backupsApi.updateSchedule(clusterId, sid, { enabled }),
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('backups.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: (sid: number) => backupsApi.deleteSchedule(clusterId, sid),
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('backups.error'))),
  })

  const scheduleName = (sid: number) => schedulesQuery.data?.find((s) => s.id === sid)?.name ?? `#${sid}`

  // A single search box for both tables: by schedule name (and its
  // target) in one, and by each run's schedule in the other.
  const [filters, setFilters] = useUrlState(['q'] as const)
  const query = filters.q.trim().toLowerCase()
  const filteredSchedules = useMemo(() => {
    const items = schedulesQuery.data ?? []
    if (!query) return items
    return items.filter((s) => s.name.toLowerCase().includes(query) || s.targetName.toLowerCase().includes(query) || s.namespaces.some((n) => n.toLowerCase().includes(query)))
  }, [schedulesQuery.data, query])
  const filteredRuns = useMemo(() => {
    const items = runsQuery.data ?? []
    if (!query) return items
    const names = new Map((schedulesQuery.data ?? []).map((s) => [s.id, s.name.toLowerCase()]))
    return items.filter((r) => (names.get(r.scheduleId) ?? `#${r.scheduleId}`).includes(query))
  }, [runsQuery.data, schedulesQuery.data, query])

  if (!validCluster || (clustersQuery.data && !cluster)) return <NotFound />

  const scheduleColumns: DataTableColumn<backupsApi.BackupSchedule>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (s) => (
        <div className="min-w-0">
          <div className="font-mono">{s.name}</div>
          <div className="truncate text-xs text-fg-muted">
            → {s.targetName} · {s.namespaces.length === 0 ? t('backups.allNamespaces') : s.namespaces.join(', ')}
          </div>
        </div>
      ),
      sortValue: (s) => s.name,
    },
    {
      key: 'policy',
      header: t('backups.policy'),
      cellClassName: 'text-xs',
      render: (s) => (
        <span className="flex flex-wrap gap-1">
          <Badge tone="neutral">{s.intervalHours === 0 ? t('backups.manualOnly') : t('backups.everyHours', { hours: s.intervalHours })}</Badge>
          <Badge tone="neutral">{t('backups.keep', { count: s.retention })}</Badge>
          <Badge tone={s.includeSecrets ? 'warning' : 'neutral'}>{s.includeSecrets ? t('backups.withSecrets') : t('backups.withoutSecrets')}</Badge>
        </span>
      ),
    },
    {
      key: 'last',
      header: t('backups.lastRun'),
      primary: true,
      cellClassName: 'text-xs text-fg-muted whitespace-nowrap',
      render: (s) => (s.lastRunAt ? <Age createdAt={s.lastRunAt} /> : '—'),
      sortValue: (s) => (s.lastRunAt ? -new Date(s.lastRunAt).getTime() : 0),
    },
    {
      key: 'enabled',
      header: t('clusterAdmin.enabled'),
      primary: true,
      render: (s) => (
        <button type="button" onClick={() => toggleMutation.mutate({ sid: s.id, enabled: !s.enabled })} className="inline-flex">
          <Badge tone={s.enabled ? 'success' : 'neutral'}>{s.enabled ? t('clusterAdmin.disable') : t('clusterAdmin.enable')}</Badge>
        </button>
      ),
      sortValue: (s) => (s.enabled ? 1 : 0),
    },
  ]

  const scheduleActions = (s: backupsApi.BackupSchedule) => (
    <RowActions>
      <MenuItem onSelect={() => runMutation.mutate(s.id)} disabled={runMutation.isPending}>
        <Play className="h-4 w-4 shrink-0" aria-hidden="true" />
        {runMutation.isPending && runMutation.variables === s.id ? t('backups.running') : t('backups.runNow')}
      </MenuItem>
      <MenuItem
        danger
        onSelect={async () => {
          if (!(await confirm({ title: t('clusterAdmin.delete'), description: t('backups.confirmDeleteSchedule', { name: s.name }), danger: true }))) return
          deleteMutation.mutate(s.id)
        }}
      >
        <Trash2 className="h-4 w-4 shrink-0" aria-hidden="true" />
        {t('clusterAdmin.delete')}
      </MenuItem>
    </RowActions>
  )

  const runColumns: DataTableColumn<backupsApi.BackupRun>[] = [
    {
      key: 'when',
      header: t('backups.when'),
      primary: true,
      cellClassName: 'text-xs whitespace-nowrap',
      render: (r) => (
        <div>
          <div>{new Date(r.startedAt).toLocaleString(locale)}</div>
          <div className="text-fg-muted">
            {scheduleName(r.scheduleId)} · {t(`backups.trigger.${r.trigger}`)}
          </div>
        </div>
      ),
    },
    {
      key: 'status',
      header: t('cluster.columns.status'),
      primary: true,
      render: (r) =>
        r.status === 'ok' ? (
          <Badge tone="success">OK</Badge>
        ) : r.status === 'running' ? (
          <Badge tone="warning">{t('backups.running')}</Badge>
        ) : (
          <span className="text-xs break-words text-danger">{r.error}</span>
        ),
    },
    {
      key: 'size',
      header: t('backups.content'),
      cellClassName: 'text-xs',
      render: (r) => (
        <div>
          {r.status === 'ok' && (
            <div>
              {t('backups.objectsCount', { count: r.objects })} · {formatBytes(r.sizeBytes, locale)}
            </div>
          )}
          {r.skipped && Object.keys(r.skipped).length > 0 && (
            <div className="text-warning" title={Object.values(r.skipped).join('\n')}>
              {t('backups.skipped', { kinds: Object.keys(r.skipped).join(', ') })}
            </div>
          )}
        </div>
      ),
    },
  ]

  const runActions = (r: backupsApi.BackupRun) =>
    r.status === 'ok' ? (
      <RowActions>
        <MenuItem onSelect={() => setRestoreRun(r)}>
          <RotateCcw className="h-4 w-4 shrink-0" aria-hidden="true" />
          {t('backups.restore')}
        </MenuItem>
        {/* Direct download from the server, not an SPA route: a real <a> link. */}
        <MenuItem asChild>
          <a href={backupsApi.downloadUrl(clusterId, r.id)}>
            <Download className="h-4 w-4 shrink-0" aria-hidden="true" />
            {t('backups.download')}
          </a>
        </MenuItem>
      </RowActions>
    ) : null

  return (
    <div>
      <PageHeader
        title={t('backups.title')}
        description={t('backups.hint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('backups.title') }]}
      />
      {cluster && !isAdmin && <p className="text-sm text-fg-muted">{t('backups.adminOnly')}</p>}
      {isAdmin && (
        <>
          {error && (
            <Alert tone="danger" className="mb-4">
              {error}
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
          <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
            <div className="min-w-0 space-y-6">
              <section>
                <h2 className="mb-2 text-sm font-semibold">{t('backups.schedules')}</h2>
                <DataTable
                  columns={scheduleColumns}
                  data={filteredSchedules}
                  rowKey={(s) => String(s.id)}
                  filtered={query !== ''}
                  isLoading={schedulesQuery.isPending}
                  error={schedulesQuery.isError ? apiErrorText(schedulesQuery.error, t('backups.error')) : undefined}
                  emptyMessage={t('backups.noSchedules')}
                  actions={scheduleActions}
                  state={schedState}
                  onStateChange={setSchedState}
                />
              </section>
              <section>
                <h2 className="mb-2 text-sm font-semibold">{t('backups.runs')}</h2>
                <DataTable
                  columns={runColumns}
                  data={filteredRuns}
                  rowKey={(r) => String(r.id)}
                  filtered={query !== ''}
                  isLoading={runsQuery.isPending}
                  error={runsQuery.isError ? apiErrorText(runsQuery.error, t('backups.error')) : undefined}
                  emptyMessage={t('backups.noRuns')}
                  actions={runActions}
                />
              </section>
            </div>
            <Card className="h-fit min-w-0 p-5">
              <h2 className="text-sm font-semibold">{t('backups.addSchedule')}</h2>
              {targetsQuery.data && targetsQuery.data.length === 0 ? (
                <p className="mt-2 text-xs text-fg-muted">
                  {t('backups.noTargetsHint')}{' '}
                  {user?.role === 'admin' && (
                    <Link to="/admin/backup-targets" className="underline">
                      {t('backups.targetsTitle')}
                    </Link>
                  )}
                </p>
              ) : (
                <form
                  onSubmit={(e: FormEvent) => {
                    e.preventDefault()
                    setError(null)
                    createMutation.mutate()
                  }}
                  className="mt-3 space-y-3"
                >
                  <Field label={t('cluster.columns.name')}>
                    <Input mono value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} required pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?" placeholder="daily" />
                  </Field>
                  <Field label={t('backups.target')}>
                    <Select value={form.targetId} onChange={(e) => setForm((f) => ({ ...f, targetId: e.target.value }))} required>
                      <option value="">—</option>
                      {(targetsQuery.data ?? []).map((x) => (
                        <option key={x.id} value={x.id}>
                          {x.name} · {x.bucket}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <Field label={t('backups.namespaces')}>
                    <Input mono list="backup-namespaces" value={form.namespaces} onChange={(e) => setForm((f) => ({ ...f, namespaces: e.target.value }))} placeholder={t('backups.namespacesPlaceholder')} />
                  </Field>
                  <datalist id="backup-namespaces">
                    {(namespacesQuery.data ?? []).map((ns) => (
                      <option key={ns.name} value={ns.name} />
                    ))}
                  </datalist>
                  <div className="grid grid-cols-2 gap-3">
                    <Field label={t('backups.interval')}>
                      <Select value={form.intervalHours} onChange={(e) => setForm((f) => ({ ...f, intervalHours: Number(e.target.value) }))}>
                        <option value={0}>{t('backups.manualOnly')}</option>
                        <option value={1}>{t('backups.everyHours', { hours: 1 })}</option>
                        <option value={6}>{t('backups.everyHours', { hours: 6 })}</option>
                        <option value={12}>{t('backups.everyHours', { hours: 12 })}</option>
                        <option value={24}>{t('backups.everyHours', { hours: 24 })}</option>
                        <option value={168}>{t('backups.everyHours', { hours: 168 })}</option>
                      </Select>
                    </Field>
                    <Field label={t('backups.retention')}>
                      <Input type="number" min={1} max={365} value={form.retention} onChange={(e) => setForm((f) => ({ ...f, retention: Number(e.target.value) }))} />
                    </Field>
                  </div>
                  <Checkbox
                    label={t('backups.includeSecrets')}
                    hint={t('backups.includeSecretsHint')}
                    checked={form.includeSecrets}
                    onChange={(e) => setForm((f) => ({ ...f, includeSecrets: e.target.checked }))}
                  />
                  <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
                    {t('backups.addSchedule')}
                  </Button>
                </form>
              )}
            </Card>
          </div>
          {restoreRun && <RestoreModal clusterId={clusterId} run={restoreRun} onClose={() => setRestoreRun(null)} onRestored={invalidate} />}
        </>
      )}
    </div>
  )
}

/** Pick objects from the archive and apply them: dry-run first, like the YAML editor. */
function RestoreModal({ clusterId, run, onClose, onRestored }: { clusterId: number; run: backupsApi.BackupRun; onClose: () => void; onRestored: () => void }) {
  const { t, i18n } = useTranslation()
  const indexQuery = useQuery({ queryKey: ['backup-index', clusterId, run.id], queryFn: () => backupsApi.getRunIndex(clusterId, run.id), staleTime: Infinity })
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [filter, setFilter] = useState('')
  const [dryRunDone, setDryRunDone] = useState(false)
  const [preview, setPreview] = useState<resources.ApplyResult[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const items = useMemo(() => {
    const all = indexQuery.data?.items ?? []
    const q = filter.trim().toLowerCase()
    return q ? all.filter((i) => i.path.toLowerCase().includes(q)) : all
  }, [indexQuery.data, filter])
  const grouped = useMemo(() => {
    const m = new Map<string, backupsApi.BackupManifestItem[]>()
    for (const it of items) {
      const k = it.namespace || t('backups.clusterScoped')
      if (!m.has(k)) m.set(k, [])
      m.get(k)!.push(it)
    }
    return Array.from(m.entries())
  }, [items, t])

  const mutation = useMutation({
    mutationFn: (dryRun: boolean) => backupsApi.restoreRun(clusterId, run.id, Array.from(selected), dryRun),
    onSuccess: (res, dryRun) => {
      setError(null)
      if (dryRun) {
        setPreview(res.applied)
        setDryRunDone(true)
      } else {
        onRestored()
        onClose()
      }
    },
    onError: (err) => {
      setError(apiErrorText(err, t('backups.error')))
      setDryRunDone(false)
    },
  })
  const toggle = (p: string) => {
    setDryRunDone(false)
    setPreview(null)
    setSelected((s) => {
      const n = new Set(s)
      if (n.has(p)) n.delete(p)
      else n.add(p)
      return n
    })
  }
  const toggleGroup = (paths: string[]) => {
    setDryRunDone(false)
    setPreview(null)
    setSelected((s) => {
      const n = new Set(s)
      const all = paths.every((p) => n.has(p))
      paths.forEach((p) => (all ? n.delete(p) : n.add(p)))
      return n
    })
  }

  return (
    <Modal
      onClose={onClose}
      size="lg"
      title={t('backups.restoreTitle', { when: formatDateTime(run.startedAt, i18n.resolvedLanguage) })}
      footer={
        <>
          <span className="mr-auto self-center text-xs text-fg-muted">{t('backups.selectedCount', { count: selected.size })}</span>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button variant="primary" disabled={mutation.isPending || selected.size === 0} onClick={() => mutation.mutate(!dryRunDone)}>
            {mutation.isPending ? t('yaml.applying') : dryRunDone ? t('yaml.apply') : t('yaml.dryRun')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-xs text-fg-muted">{t('backups.restoreHint')}</p>
        {indexQuery.data && !indexQuery.data.includeSecrets && <Alert tone="warning">{t('backups.restoreNoSecrets')}</Alert>}
        <SearchInput value={filter} onChange={setFilter} placeholder={t('common.searchPlaceholder')} />
        {indexQuery.isPending && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
        {grouped.map(([ns, list]) => (
          <div key={ns} className="rounded-md border border-border">
            <button type="button" onClick={() => toggleGroup(list.map((i) => i.path))} className="flex w-full items-center justify-between px-3 py-1.5 text-left text-sm font-medium hover:bg-surface-hover">
              <span className="font-mono">{ns}</span>
              <span className="text-xs text-fg-muted">{list.length}</span>
            </button>
            <ul className="max-h-56 overflow-y-auto border-t border-border text-xs">
              {list.map((it) => (
                <li key={it.path}>
                  <Checkbox
                    className="cursor-pointer items-center px-3 py-1 text-xs hover:bg-surface-hover"
                    checked={selected.has(it.path)}
                    onChange={() => toggle(it.path)}
                    label={
                      <>
                        <span className="text-fg-muted">{it.kind}</span> <span className="font-mono">{it.name}</span>
                      </>
                    }
                  />
                </li>
              ))}
            </ul>
          </div>
        ))}
        {preview && (
          <div className="rounded-md border border-border bg-neutral-soft px-3 py-2 text-sm">
            <p className="font-medium">{t('yaml.dryRunOk')}</p>
            <ul className="mt-1 space-y-0.5 font-mono text-xs">
              {preview.map((r) => (
                <li key={`${r.kind}/${r.namespace}/${r.name}`}>
                  {r.kind} {r.namespace ? `${r.namespace}/` : ''}
                  {r.name} · {r.operation}
                </li>
              ))}
            </ul>
          </div>
        )}
        {error && <Alert tone="danger">{error}</Alert>}
      </div>
    </Modal>
  )
}
