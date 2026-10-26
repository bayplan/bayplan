import { useMemo, useState, type FormEvent } from 'react'
import { useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Eye, GitBranch, Pause, Play, RefreshCw, Trash2, X } from 'lucide-react'
import * as gitopsApi from '../api/gitops'
import { listClusters } from '../api/clusters'
import { SearchInput } from '../components/SearchInput'
import { Age, Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, Modal, PageHeader, RowActions, Select, Textarea, Toolbar, useConfirm, type BadgeTone, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { NotFound } from './NotFound'
import { apiErrorText } from '../lib/errors'
import { useCopy } from '../hooks/useCopy'

// Configuration, not cluster resources: one minute is enough.
const POLL_INTERVAL_MS = 60000
const ICON = 'h-4 w-4 shrink-0'

function statusTone(s: gitopsApi.GitSource): BadgeTone {
  if (!s.enabled) return 'neutral'
  if (s.lastStatus === 'synced') return 'success'
  if (s.lastStatus === 'error') return 'danger'
  return 'warning'
}

/**
 * GitOps sources of the cluster: repo + branch + path applied with server-side
 * apply every N seconds or by webhook. No prune on purpose (what is removed
 * from the repo is not removed from the cluster) and no health: a full GitOps
 * controller is for that.
 */
export function GitOpsSources() {
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

  const sourcesQuery = useQuery({
    queryKey: ['gitops', clusterId],
    queryFn: () => gitopsApi.listGitSources(clusterId),
    enabled: validCluster,
    refetchInterval: POLL_INTERVAL_MS,
  })
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['gitops', clusterId] })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [actionError, setActionError] = useState<string | null>(null)
  const [created, setCreated] = useState<gitopsApi.CreatedGitSource | null>(null)
  const [detail, setDetail] = useState<gitopsApi.GitSource | null>(null)

  const [form, setForm] = useState<gitopsApi.CreateGitSourcePayload>({
    name: '',
    repoUrl: '',
    branch: 'main',
    path: '.',
    namespace: namespaces.length === 1 ? namespaces[0] : '',
    username: '',
    token: '',
    intervalSeconds: 300,
    authType: 'https',
    sshKnownHosts: '',
    prune: false,
    kustomize: false,
  })
  const [formError, setFormError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: (p: gitopsApi.CreateGitSourcePayload) => gitopsApi.createGitSource(clusterId, p),
    onSuccess: (res) => {
      invalidate()
      setCreated(res)
      setForm((f) => ({ ...f, name: '', repoUrl: '', token: '' }))
      setFormError(null)
    },
    onError: (err) => setFormError(apiErrorText(err, t('gitops.error'))),
  })
  const toggleMutation = useMutation({
    mutationFn: ({ sid, enabled }: { sid: number; enabled: boolean }) => gitopsApi.setGitSourceEnabled(clusterId, sid, enabled),
    onSuccess: invalidate,
    onError: (err) => setActionError(apiErrorText(err, t('gitops.error'))),
  })
  const syncMutation = useMutation({
    mutationFn: (sid: number) => gitopsApi.syncGitSource(clusterId, sid),
    onSuccess: invalidate,
    onError: (err) => {
      invalidate()
      setActionError(apiErrorText(err, t('gitops.error')))
    },
  })
  const deleteMutation = useMutation({
    mutationFn: (sid: number) => gitopsApi.deleteGitSource(clusterId, sid),
    onSuccess: invalidate,
    onError: (err) => setActionError(apiErrorText(err, t('gitops.error'))),
  })

  const filtered = useMemo(() => {
    const items = sourcesQuery.data ?? []
    if (!query) return items
    return items.filter((s) => s.name.toLowerCase().includes(query) || s.repoUrl.toLowerCase().includes(query) || (s.namespace ?? '').toLowerCase().includes(query))
  }, [sourcesQuery.data, query])

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setFormError(null)
    createMutation.mutate({
      ...form,
      namespace: form.namespace || undefined,
      username: form.username || undefined,
      token: form.token || undefined,
      sshKnownHosts: form.authType === 'ssh' ? form.sshKnownHosts || undefined : undefined,
    })
  }

  const columns: DataTableColumn<gitopsApi.GitSource>[] = [
    {
      key: 'status',
      header: t('cluster.columns.status'),
      primary: true,
      render: (s) => (
        <Badge tone={statusTone(s)}>{s.enabled ? t(`gitops.status.${s.lastStatus}`) : t('gitops.paused')}</Badge>
      ),
      sortValue: (s) => (s.enabled ? s.lastStatus : 'zz'),
    },
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (s) => (
        <button type="button" onClick={() => setDetail(s)} className="font-mono text-link hover:underline">
          {s.name}
        </button>
      ),
      sortValue: (s) => s.name,
    },
    {
      key: 'repo',
      header: t('gitops.repo'),
      cellClassName: 'font-mono text-xs text-fg-muted',
      render: (s) => (
        <div className="min-w-0">
          <div className="truncate">{s.repoUrl}</div>
          <div className="flex items-center gap-1">
            <GitBranch className="h-3 w-3" aria-hidden="true" />
            {s.branch}
            {s.path !== '.' && ` · /${s.path}`}
            {s.namespace && ` · ns ${s.namespace}`}
          </div>
          <div className="mt-0.5 flex flex-wrap gap-1">
            {s.authType === 'ssh' && <Badge tone="neutral">SSH</Badge>}
            {s.authType === 'ssh' && !s.hasHostKey && <Badge tone="warning">{t('gitops.hostKeyUnverified')}</Badge>}
            {s.kustomize && <Badge tone="neutral">Kustomize</Badge>}
            {s.prune && <Badge tone="warning">{t('gitops.prune')}</Badge>}
          </div>
        </div>
      ),
      sortValue: (s) => s.repoUrl,
    },
    {
      key: 'commit',
      header: t('gitops.commit'),
      cellClassName: 'font-mono text-xs',
      render: (s) => (s.lastCommit ? s.lastCommit.slice(0, 8) : '—'),
    },
    {
      key: 'lastSync',
      header: t('gitops.lastSync'),
      primary: true,
      cellClassName: 'whitespace-nowrap text-fg-muted',
      render: (s) => (s.lastSyncAt ? <Age createdAt={s.lastSyncAt} /> : '—'),
      sortValue: (s) => (s.lastSyncAt ? -new Date(s.lastSyncAt).getTime() : 0),
    },
    {
      key: 'interval',
      header: t('gitops.interval'),
      cellClassName: 'text-xs text-fg-muted',
      render: (s) => t('gitops.everySeconds', { count: s.intervalSeconds }),
      sortValue: (s) => s.intervalSeconds,
    },
  ]

  const rowActions = (s: gitopsApi.GitSource) => (
    <RowActions>
      <MenuItem onSelect={() => setDetail(s)}>
        <Eye className={ICON} aria-hidden="true" />
        {t('helm.details')}
      </MenuItem>
      {canWrite && (
        <>
          <MenuItem onSelect={() => syncMutation.mutate(s.id)} disabled={syncMutation.isPending}>
            <RefreshCw className={`${ICON} ${syncMutation.isPending && syncMutation.variables === s.id ? 'animate-spin' : ''}`} aria-hidden="true" />
            {t('gitops.syncNow')}
          </MenuItem>
          <MenuItem onSelect={() => toggleMutation.mutate({ sid: s.id, enabled: !s.enabled })}>
            {s.enabled ? <Pause className={ICON} aria-hidden="true" /> : <Play className={ICON} aria-hidden="true" />}
            {s.enabled ? t('gitops.pause') : t('gitops.resume')}
          </MenuItem>
        </>
      )}
      {isAdmin && (
        <MenuItem
          danger
          onSelect={async () => {
            if (!(await confirm({ title: t('clusterAdmin.delete'), description: t('gitops.confirmDelete', { name: s.name }), danger: true }))) return
            deleteMutation.mutate(s.id)
          }}
        >
          <Trash2 className={ICON} aria-hidden="true" />
          {t('clusterAdmin.delete')}
        </MenuItem>
      )}
    </RowActions>
  )

  if (!validCluster) return <NotFound />

  return (
    <div>
      {created && (
        <Modal
          onClose={() => setCreated(null)}
          variant="center"
          size="md"
          title={t('gitops.createdTitle', { name: created.name })}
          footer={
            <Button variant="primary" onClick={() => setCreated(null)}>
              {t('common.close')}
            </Button>
          }
        >
          <p className="text-sm text-fg-muted">{t('gitops.webhookHint')}</p>
          <WebhookBox path={created.webhookPath} token={created.webhookToken} />
        </Modal>
      )}
      {detail && (
        <Modal onClose={() => setDetail(null)} size="lg" title={<span className="font-mono">{detail.name}</span>}>
          <div className="space-y-4 text-sm">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
              <dt className="text-fg-muted">{t('gitops.repo')}</dt>
              <dd className="font-mono break-all">{detail.repoUrl}</dd>
              <dt className="text-fg-muted">{t('gitops.branch')}</dt>
              <dd className="font-mono">{detail.branch}</dd>
              <dt className="text-fg-muted">{t('gitops.path')}</dt>
              <dd className="font-mono">{detail.path}</dd>
              <dt className="text-fg-muted">{t('gitops.commit')}</dt>
              <dd className="font-mono">{detail.lastCommit ?? '—'}</dd>
              <dt className="text-fg-muted">Webhook</dt>
              <dd className="font-mono break-all">
                POST {window.location.origin}/api/gitops/webhook/{detail.id}
                <span className="block text-fg-muted">{t('gitops.webhookTokenHidden')}</span>
              </dd>
            </dl>
            {detail.lastError && (
              <Alert tone="danger" className="text-xs">
                {detail.lastError}
              </Alert>
            )}
            <div>
              <h3 className="text-xs font-semibold text-fg-muted uppercase">{t('gitops.lastApplied')}</h3>
              {detail.lastApplied && detail.lastApplied.length > 0 ? (
                <ul className="mt-1 space-y-0.5 font-mono text-xs">
                  {detail.lastApplied.map((a) => (
                    <li key={`${a.kind}/${a.namespace}/${a.name}`} className="flex items-center gap-2">
                      <Badge tone={a.operation === 'created' ? 'success' : 'neutral'}>{t(`yaml.op.${a.operation}`)}</Badge>
                      {a.kind} {a.namespace ? `${a.namespace}/` : ''}
                      {a.name}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="mt-1 text-xs text-fg-muted">—</p>
              )}
            </div>
            {detail.lastPruned && detail.lastPruned.length > 0 && (
              <div>
                <h3 className="text-xs font-semibold text-fg-muted uppercase">{t('gitops.lastPruned')}</h3>
                <ul className="mt-1 space-y-0.5 font-mono text-xs">
                  {detail.lastPruned.map((a) => (
                    <li key={`${a.kind}/${a.namespace}/${a.name}`} className="flex items-center gap-2">
                      <Badge tone="danger">{t('gitops.pruned')}</Badge>
                      {a.kind} {a.namespace ? `${a.namespace}/` : ''}
                      {a.name}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </Modal>
      )}

      <PageHeader
        title={t('gitops.title')}
        description={t('gitops.hint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('gitops.title') }]}
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

      <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
        <div className="min-w-0">
          <DataTable
            columns={columns}
            data={filtered}
            rowKey={(s) => String(s.id)}
            filtered={query !== ''}
            isLoading={sourcesQuery.isPending}
            error={sourcesQuery.isError ? apiErrorText(sourcesQuery.error, t('gitops.error')) : undefined}
            emptyMessage={t('gitops.empty')}
            actions={rowActions}
            state={tableState}
            onStateChange={setTableState}
          />
        </div>

        {canWrite && (
          <Card className="h-fit min-w-0 p-5">
            <h2 className="text-sm font-semibold">{t('gitops.addTitle')}</h2>
            <form onSubmit={handleSubmit} className="mt-3 space-y-3">
              <Field label={t('cluster.columns.name')}>
                <Input mono value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} required pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?" placeholder="my-app" />
              </Field>
              <Field label={t('gitops.repo')}>
                <Input mono value={form.repoUrl} onChange={(e) => setForm((f) => ({ ...f, repoUrl: e.target.value }))} required placeholder="https://github.com/org/repo.git" />
              </Field>
              <div className="grid grid-cols-2 gap-3">
                <Field label={t('gitops.branch')}>
                  <Input mono value={form.branch ?? ''} onChange={(e) => setForm((f) => ({ ...f, branch: e.target.value }))} />
                </Field>
                <Field label={t('gitops.path')}>
                  <Input mono value={form.path ?? ''} onChange={(e) => setForm((f) => ({ ...f, path: e.target.value }))} placeholder="deploy/k8s" />
                </Field>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <Field label={t('gitops.defaultNamespace')}>
                  <Input mono value={form.namespace ?? ''} onChange={(e) => setForm((f) => ({ ...f, namespace: e.target.value }))} placeholder="default" />
                </Field>
                <Field label={t('gitops.intervalSeconds')}>
                  <Input type="number" min={30} max={86400} value={form.intervalSeconds ?? 300} onChange={(e) => setForm((f) => ({ ...f, intervalSeconds: Number(e.target.value) }))} />
                </Field>
              </div>
              <Field label={t('gitops.authType')}>
                <Select value={form.authType ?? 'https'} onChange={(e) => setForm((f) => ({ ...f, authType: e.target.value as 'https' | 'ssh', token: '' }))}>
                  <option value="https">HTTPS</option>
                  <option value="ssh">SSH</option>
                </Select>
              </Field>
              {form.authType === 'ssh' ? (
                <>
                  <Field label={t('gitops.sshKey')}>
                    <Textarea mono value={form.token ?? ''} onChange={(e) => setForm((f) => ({ ...f, token: e.target.value }))} required rows={4} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
                  </Field>
                  <Field label={t('gitops.knownHosts')} hint={t('gitops.sshHint')}>
                    <Textarea mono value={form.sshKnownHosts ?? ''} onChange={(e) => setForm((f) => ({ ...f, sshKnownHosts: e.target.value }))} required rows={2} placeholder="github.com ssh-ed25519 AAAA…" className="min-h-16" />
                  </Field>
                </>
              ) : (
                <>
                  <div className="grid grid-cols-2 gap-3">
                    <Field label={t('login.username')}>
                      <Input value={form.username ?? ''} onChange={(e) => setForm((f) => ({ ...f, username: e.target.value }))} autoComplete="off" />
                    </Field>
                    <Field label={t('gitops.token')}>
                      <Input type="password" value={form.token ?? ''} onChange={(e) => setForm((f) => ({ ...f, token: e.target.value }))} autoComplete="new-password" />
                    </Field>
                  </div>
                  <p className="text-xs text-fg-muted">{t('gitops.tokenHint')}</p>
                </>
              )}
              <Checkbox label={t('gitops.kustomize')} checked={form.kustomize ?? false} onChange={(e) => setForm((f) => ({ ...f, kustomize: e.target.checked }))} />
              <Checkbox label={t('gitops.prune')} hint={t('gitops.pruneHint')} checked={form.prune ?? false} onChange={(e) => setForm((f) => ({ ...f, prune: e.target.checked }))} />
              {formError && <Alert tone="danger">{formError}</Alert>}
              <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
                {t('gitops.add')}
              </Button>
            </form>
          </Card>
        )}
      </div>
    </div>
  )
}

function WebhookBox({ path, token }: { path: string; token: string }) {
  const { t } = useTranslation()
  const { copy, copiedKey: copied } = useCopy()
  const url = `${window.location.origin}${path}`
  return (
    <div className="mt-3 space-y-2 text-xs">
      <div>
        <span className="text-fg-muted">URL</span>
        <div className="mt-0.5 flex items-center gap-2">
          <code className="flex-1 truncate rounded-md bg-neutral-soft px-2 py-1.5 font-mono">POST {url}</code>
          <Button size="sm" onClick={() => void copy(url, 'url')}>
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
            {copied === 'url' ? t('common.copied') : t('common.copy')}
          </Button>
        </div>
      </div>
      <div>
        <span className="text-fg-muted">{t('gitops.webhookSecret', { headers: 'X-Webhook-Token / X-Gitlab-Token / HMAC X-Hub-Signature-256' })}</span>
        <div className="mt-0.5 flex items-center gap-2">
          <code className="flex-1 truncate rounded-md bg-neutral-soft px-2 py-1.5 font-mono">{token}</code>
          <Button size="sm" onClick={() => void copy(token, 'token')}>
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
            {copied === 'token' ? t('common.copied') : t('common.copy')}
          </Button>
        </div>
      </div>
    </div>
  )
}
