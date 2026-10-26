import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy, Download, FileCode, Pencil, Power, RefreshCw, Search, ShieldCheck, Trash2 } from 'lucide-react'
import * as clustersApi from '../api/clusters'
import * as cloudApi from '../api/cloud'
import { Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, Modal, PageHeader, RowActions, SegmentedControl, Select, StatusDot, Textarea, Toolbar, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { clusterConnection, connectionLabelKey, connectionTone } from '../lib/status'
import { apiErrorText } from '../lib/errors'
import { useCopy } from '../hooks/useCopy'
import { formatDateTime, formatTime } from '../lib/dates'

type CredentialMode = 'agent' | 'cloud' | 'kubeconfig' | 'token' | 'clientCert'


/**
 * Connection fields shared by create and edit. Three ways to authenticate
 * because that is what each client brings: a full kubeconfig (the most common,
 * and what kubeadm/k3s/RKE2/Talos export), a ServiceAccount token (what the
 * guide recommends: scoped permissions), or a loose client certificate.
 */
function ConnectionFields({
  mode,
  setMode,
  form,
  setForm,
  editing,
}: {
  mode: CredentialMode
  setMode: (m: CredentialMode) => void
  form: clustersApi.ClusterPayload
  setForm: (f: (prev: clustersApi.ClusterPayload) => clustersApi.ClusterPayload) => void
  editing: boolean
}) {
  const { t } = useTranslation()
  const set = <K extends keyof clustersApi.ClusterPayload>(key: K, value: clustersApi.ClusterPayload[K]) =>
    setForm((f) => ({ ...f, [key]: value }))
  const modes = (editing ? ['kubeconfig', 'token', 'clientCert'] : ['agent', 'cloud', 'kubeconfig', 'token', 'clientCert']) as CredentialMode[]

  return (
    <>
      <SegmentedControl
        size="sm"
        options={modes.map((m) => ({ value: m, label: t(`clusterAdmin.mode.${m}`) }))}
        value={mode}
        onChange={setMode}
        className="flex w-full [&>button]:flex-1"
      />

      {mode === 'agent' ? (
        <div className="space-y-2 rounded-md bg-neutral-soft px-3 py-3 text-xs text-fg-muted">
          <div className="flex gap-4 text-sm text-fg">
            <label className="flex items-center gap-1.5">
              <input type="radio" name="kind" checked={(form.kind ?? 'kubernetes') === 'kubernetes'} onChange={() => set('kind', 'kubernetes')} />
              Kubernetes
            </label>
            <label className="flex items-center gap-1.5">
              <input type="radio" name="kind" checked={form.kind === 'docker'} onChange={() => set('kind', 'docker')} />
              Docker / Podman
            </label>
          </div>
          <p>{form.kind === 'docker' ? t('clusterAdmin.dockerHint') : t('clusterAdmin.agentHint')}</p>
          <p>{form.kind === 'docker' ? t('clusterAdmin.dockerHint2') : t('clusterAdmin.agentHint2')}</p>
          <Checkbox
            label={t('clusterAdmin.agentInsecure')}
            hint={<span className="text-warning">{t('clusterAdmin.insecureHint')}</span>}
            checked={form.insecureSkipTLSVerify ?? false}
            onChange={(e) => set('insecureSkipTLSVerify', e.target.checked)}
          />
          {form.kind !== 'docker' && <RBACModeField value={form.rbacMode ?? 'scoped'} onChange={(v) => set('rbacMode', v)} />}
        </div>
      ) : mode === 'cloud' ? (
        <>
          <CloudPicker form={form} setForm={setForm} />
          <RBACModeField value={form.rbacMode ?? 'scoped'} onChange={(v) => set('rbacMode', v)} />
        </>
      ) : mode === 'kubeconfig' ? (
        <Field label={t('clusterAdmin.kubeconfig')} hint={t('clusterAdmin.kubeconfigHint')}>
          <Textarea
            mono
            value={form.kubeconfig ?? ''}
            onChange={(e) => set('kubeconfig', e.target.value)}
            required={!editing}
            rows={8}
            placeholder={'apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://…'}
          />
        </Field>
      ) : (
        <>
          <Field label={t('clusterAdmin.apiEndpoint')}>
            <Input
              mono
              value={form.apiEndpoint ?? ''}
              onChange={(e) => set('apiEndpoint', e.target.value)}
              placeholder="https://api.cluster.example:6443"
              required={!editing}
            />
          </Field>
          <Field label={t('clusterAdmin.caCert')}>
            <Textarea
              mono
              value={form.caCert ?? ''}
              onChange={(e) => set('caCert', e.target.value)}
              placeholder="-----BEGIN CERTIFICATE-----"
              required={!editing && !form.insecureSkipTLSVerify}
              disabled={form.insecureSkipTLSVerify}
              rows={4}
            />
          </Field>
          <Checkbox
            label={t('clusterAdmin.insecure')}
            hint={<span className="text-warning">{t('clusterAdmin.insecureHint')}</span>}
            checked={form.insecureSkipTLSVerify ?? false}
            onChange={(e) => set('insecureSkipTLSVerify', e.target.checked)}
          />
          {mode === 'token' ? (
            <Field label={t('clusterAdmin.saToken')} hint={t('clusterAdmin.saTokenHint')}>
              <Textarea
                mono
                value={form.saToken ?? ''}
                onChange={(e) => set('saToken', e.target.value)}
                required={!editing}
                rows={3}
                placeholder={editing ? t('clusterAdmin.keepCredential') : ''}
              />
            </Field>
          ) : (
            <>
              <Field label={t('clusterAdmin.clientCert')}>
                <Textarea
                  mono
                  value={form.clientCert ?? ''}
                  onChange={(e) => set('clientCert', e.target.value)}
                  required={!editing}
                  rows={3}
                  placeholder="-----BEGIN CERTIFICATE-----"
                />
              </Field>
              <Field label={t('clusterAdmin.clientKey')}>
                <Textarea
                  mono
                  value={form.clientKey ?? ''}
                  onChange={(e) => set('clientKey', e.target.value)}
                  required={!editing}
                  rows={3}
                  placeholder={editing ? t('clusterAdmin.keepCredential') : '-----BEGIN PRIVATE KEY-----'}
                />
              </Field>
            </>
          )}
        </>
      )}
    </>
  )
}

/**
 * Create via cloud credential: pick a credential, discover the provider's
 * clusters and tick one. The display name is filled with the cluster's if the
 * admin has not typed another. Discovery is manual (button) because it costs
 * one call per region and cluster on AWS.
 */
/**
 * Permissions the panel generates in the cluster (agent and cloud). "Scoped"
 * is the safe mode and requires reapplying the RBAC when the panel learns new
 * kinds; "cluster-admin" is install and forget.
 */
function RBACModeField({ value, onChange }: { value: clustersApi.RBACMode; onChange: (v: clustersApi.RBACMode) => void }) {
  const { t } = useTranslation()
  return (
    <div className="space-y-1 text-sm text-fg">
      <span className="block text-sm">{t('clusterAdmin.rbacMode')}</span>
      <label className="flex items-start gap-2">
        <input type="radio" name="rbacMode" className="mt-1" checked={value === 'scoped'} onChange={() => onChange('scoped')} />
        <span>
          {t('clusterAdmin.rbacScoped')}
          <span className="mt-0.5 block text-xs text-fg-muted">{t('clusterAdmin.rbacScopedHint')}</span>
        </span>
      </label>
      <label className="flex items-start gap-2">
        <input type="radio" name="rbacMode" className="mt-1" checked={value === 'cluster-admin'} onChange={() => onChange('cluster-admin')} />
        <span>
          {t('clusterAdmin.rbacClusterAdmin')}
          <span className="mt-0.5 block text-xs text-warning">{t('clusterAdmin.rbacClusterAdminHint')}</span>
        </span>
      </label>
    </div>
  )
}

function CloudPicker({
  form,
  setForm,
}: {
  form: clustersApi.ClusterPayload
  setForm: (f: (prev: clustersApi.ClusterPayload) => clustersApi.ClusterPayload) => void
}) {
  const { t } = useTranslation()
  const credsQuery = useQuery({ queryKey: ['cloud-credentials'], queryFn: cloudApi.listCloudCredentials })
  const credId = form.cloudCredentialId
  const discoverQuery = useQuery({
    queryKey: ['cloud-discover', credId],
    queryFn: () => cloudApi.discoverCloudClusters(credId as number),
    enabled: false,
    retry: false,
  })
  const selectedKey = form.cloudRef ? JSON.stringify(form.cloudRef) : ''

  if (credsQuery.data && credsQuery.data.length === 0) {
    return (
      <p className="rounded-md bg-neutral-soft px-3 py-3 text-xs text-fg-muted">
        {t('clusterAdmin.cloudNoCredentials')}{' '}
        <Link to="/admin/cloud-credentials" className="underline">
          {t('cloud.title')}
        </Link>
      </p>
    )
  }

  return (
    <div className="space-y-3">
      <p className="text-xs text-fg-muted">{t('clusterAdmin.cloudHint')}</p>
      <label className="block text-sm font-medium text-fg">
        {t('clusterAdmin.cloudCredential')}
        {/* Not a Field: the control and the discover button go on the same row. */}
        <div className="mt-1 flex gap-2">
          <Select
            value={credId ?? ''}
            onChange={(e) => setForm((f) => ({ ...f, cloudCredentialId: e.target.value ? Number(e.target.value) : undefined, cloudRef: undefined }))}
            required
            className="flex-1"
          >
            <option value="">—</option>
            {(credsQuery.data ?? []).map((c) => (
              <option key={c.id} value={c.id}>
                {c.name} ({t(`cloud.provider.${c.provider}`)})
              </option>
            ))}
          </Select>
          <Button type="button" onClick={() => void discoverQuery.refetch()} disabled={!credId || discoverQuery.isFetching}>
            <Search className="h-3.5 w-3.5" aria-hidden="true" />
            {discoverQuery.isFetching ? t('clusterAdmin.cloudSearching') : t('clusterAdmin.cloudSearch')}
          </Button>
        </div>
      </label>
      {discoverQuery.error && <Alert tone="danger">{apiErrorText(discoverQuery.error, t('clusterAdmin.error'))}</Alert>}
      {discoverQuery.data && discoverQuery.data.length === 0 && <p className="text-sm text-fg-muted">{t('clusterAdmin.cloudNoneFound')}</p>}
      {discoverQuery.data && discoverQuery.data.length > 0 && (
        <ul className="max-h-72 space-y-1 overflow-y-auto rounded-md border border-border p-1">
          {discoverQuery.data.map((d) => {
            const key = JSON.stringify(d.ref)
            const added = d.clusterId !== undefined
            return (
              <li key={key}>
                <label className={`flex cursor-pointer items-start gap-2 rounded px-2 py-1.5 text-sm hover:bg-neutral-soft ${added ? 'opacity-60' : ''}`}>
                  <input
                    type="radio"
                    name="cloudCluster"
                    className="mt-1"
                    disabled={added}
                    checked={selectedKey === key}
                    onChange={() => setForm((f) => ({ ...f, cloudRef: d.ref, displayName: f.displayName || d.name }))}
                  />
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-1">
                      <span className="font-mono">{d.name}</span>
                      {d.version && <Badge tone="neutral">{d.version}</Badge>}
                      {d.status && <Badge tone={/ACTIVE|RUNNING|Succeeded/i.test(d.status) ? 'success' : 'warning'}>{d.status}</Badge>}
                      {d.private && <Badge tone="warning">{t('clusterAdmin.cloudPrivate')}</Badge>}
                      {added && <Badge tone="neutral">{t('clusterAdmin.cloudAlreadyAdded')}</Badge>}
                    </span>
                    <span className="block truncate text-xs text-fg-muted">
                      {d.location}
                      {d.endpoint ? ` · ${d.endpoint}` : ''}
                    </span>
                  </span>
                </label>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

const EMPTY_FORM: clustersApi.ClusterPayload = { displayName: '', description: '' }

export function ClusterAdmin() {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['clusters'] })

  // The connection state changes on its own (health check every 30 s in the
  // backend); this is a configuration view, a minute of delay in the status
  // dot is acceptable.
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: clustersApi.listClusters, refetchInterval: 60000 })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [filters, setFilters] = useUrlState(['q'] as const)

  const [form, setForm] = useState<clustersApi.ClusterPayload>(EMPTY_FORM)
  const [mode, setMode] = useState<CredentialMode>('agent')
  const [error, setError] = useState<string | null>(null)
  // Errors of the row actions, shown above the table: the create form's alert
  // is out of view (and on mobile, a whole screen away) from the row clicked.
  const [rowError, setRowError] = useState<string | null>(null)
  const [onboarding, setOnboarding] = useState<clustersApi.AgentOnboarding | null>(null)
  const [cloudOnboarding, setCloudOnboarding] = useState<clustersApi.CloudOnboarding | null>(null)

  const [editTarget, setEditTarget] = useState<clustersApi.Cluster | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<clustersApi.Cluster | null>(null)
  const [confirmText, setConfirmText] = useState('')
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: clustersApi.createCluster,
    onSuccess: (res) => {
      invalidate()
      setForm(EMPTY_FORM)
      setError(null)
      if (clustersApi.isAgentOnboarding(res)) setOnboarding(res)
      else if (clustersApi.isCloudOnboarding(res)) setCloudOnboarding(res)
    },
    onError: (err) => setError(apiErrorText(err, t('clusterAdmin.error'))),
  })

  const manifestMutation = useMutation({
    mutationFn: (id: number) => clustersApi.regenerateAgentManifest(id),
    onSuccess: (res) => {
      setRowError(null)
      setOnboarding(res)
    },
    onError: (err) => setRowError(apiErrorText(err, t('clusterAdmin.error'))),
  })

  const rbacMutation = useMutation({
    mutationFn: (id: number) => clustersApi.getCloudRBAC(id),
    onSuccess: (res) => {
      setRowError(null)
      setCloudOnboarding(res)
    },
    onError: (err) => setRowError(apiErrorText(err, t('clusterAdmin.error'))),
  })

  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => clustersApi.setClusterEnabled(id, enabled),
    onSuccess: () => {
      setRowError(null)
      invalidate()
    },
    onError: (err) => setRowError(apiErrorText(err, t('clusterAdmin.error'))),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => clustersApi.deleteCluster(id),
    onSuccess: () => {
      invalidate()
      setDeleteTarget(null)
      setConfirmText('')
      setDeleteError(null)
    },
    // The modal is not closed: the admin sees the failure right there and can
    // retry without having to find the cluster in the table again.
    onError: (err) => setDeleteError(apiErrorText(err, t('clusterAdmin.error'))),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate(cleanPayload(form, mode))
  }

  const search = filters.q.trim().toLowerCase()
  const clusters = (clustersQuery.data ?? []).filter(
    (c) => !search || c.displayName.toLowerCase().includes(search) || (c.description ?? '').toLowerCase().includes(search) || (c.apiEndpoint ?? '').toLowerCase().includes(search),
  )

  const columns: DataTableColumn<clustersApi.Cluster>[] = [
    {
      key: 'status',
      header: t('clusterAdmin.status'),
      render: (c) => {
        const conn = clusterConnection(c)
        return (
          <span className="flex items-center gap-2" title={c.status.lastError}>
            <StatusDot tone={connectionTone(conn)} title={t(connectionLabelKey(conn))} />
            <span className="text-xs">{t(connectionLabelKey(conn))}</span>
          </span>
        )
      },
      sortValue: (c) => clusterConnection(c),
      primary: true,
    },
    {
      key: 'name',
      header: t('cluster.columns.name'),
      // On mobile (endpoint hidden) this column is the one that absorbs the
      // spare width and truncates the description; on desktop endpoint does.
      cellClassName: 'max-w-0 w-full md:w-auto md:max-w-none',
      render: (c) => (
        <div className="min-w-0">
          <div className="font-medium">{c.displayName}</div>
          {c.description && <div className="truncate text-xs text-fg-muted">{c.description}</div>}
        </div>
      ),
      sortValue: (c) => c.displayName,
      primary: true,
    },
    {
      key: 'endpoint',
      header: t('clusterAdmin.endpoint'),
      // max-w-0 + w-full: the column keeps the spare width and the endpoint is
      // cut with truncate. Without it a long URL sets the table's minimum
      // width, which overflows its grid column.
      cellClassName: 'max-w-0 w-full text-fg-muted text-xs font-mono',
      render: (c) => (
        <div>
          <div className="truncate">
            {c.connector === 'agent' ? (c.agentLastSeen ? t('clusterAdmin.agentSeen') : t('clusterAdmin.agentNeverSeen')) : c.apiEndpoint}
            {c.rbacMode === 'cluster-admin' && (
              <Badge tone="warning" className="ml-2">
                cluster-admin
              </Badge>
            )}
            {c.agentRbac && c.agentLastSeen && (
              <span className="ml-2 inline-flex" title={c.agentRbac.error ?? (c.agentRbac.appliedAt ? formatDateTime(c.agentRbac.appliedAt, i18n.resolvedLanguage) : '')}>
                <Badge tone={c.agentRbac.upToDate ? 'success' : 'warning'}>
                  {c.agentRbac.upToDate ? t('clusterAdmin.rbacUpToDate') : c.agentRbac.error ? t('clusterAdmin.rbacError') : t('clusterAdmin.rbacPending')}
                </Badge>
              </span>
            )}
          </div>
          <div className="mt-0.5 flex flex-wrap gap-1">
            {c.status.serverVersion && <Badge tone="neutral">{c.status.serverVersion}</Badge>}
            {c.kind === 'docker' && <Badge tone="neutral">Docker</Badge>}
            <Badge tone={c.connector !== 'direct' ? 'success' : 'neutral'}>{t(`clusterAdmin.mode.${c.authType}`)}</Badge>
            {c.cloud && <Badge tone="neutral">{t(`cloud.provider.${c.cloud.provider}`)}</Badge>}
            {c.insecureSkipTLSVerify && <Badge tone="warning">{t('clusterAdmin.insecureBadge')}</Badge>}
          </div>
        </div>
      ),
      sortValue: (c) => c.apiEndpoint,
    },
    {
      key: 'enabled',
      header: t('clusterAdmin.enabled'),
      // Ascending leaves the disabled ones on top (false < true): they are the
      // ones to look at in an administration screen.
      sortValue: (c) => (c.enabled ? 1 : 0),
      render: (c) => <Badge tone={c.enabled ? 'success' : 'neutral'}>{c.enabled ? t('common.enabled') : t('common.disabled')}</Badge>,
    },
  ]

  // Per-cluster errors below the table: a tooltip is not discoverable and the
  // error text is exactly what the admin needs to read (401, 403, DNS…).
  const withErrors = (clustersQuery.data ?? []).filter((c) => c.enabled && c.status.lastError)

  return (
    <div>
      <PageHeader
        title={t('clusterAdmin.title')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('clusterAdmin.title') }]}
      />
      <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
        <div className="min-w-0 space-y-4">
          <Toolbar>
            <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v })} />
            {search && (
              <Button variant="ghost" size="sm" onClick={() => setFilters({ q: '' })}>
                {t('common.clearFilters')}
              </Button>
            )}
          </Toolbar>
          {rowError && <Alert tone="danger">{rowError}</Alert>}
          <DataTable
            columns={columns}
            data={clusters}
            rowKey={(c) => String(c.id)}
            emptyMessage={t('clusterAdmin.empty')}
            isLoading={clustersQuery.isPending}
            error={clustersQuery.isError ? apiErrorText(clustersQuery.error, t('common.error')) : undefined}
            filtered={search !== ''}
            state={tableState}
            onStateChange={setTableState}
            actions={(c) => (
              <RowActions>
                {c.connector === 'agent' && (
                  <MenuItem onSelect={() => manifestMutation.mutate(c.id)}>
                    <FileCode className="h-4 w-4" aria-hidden="true" />
                    {t('clusterAdmin.manifest')}
                  </MenuItem>
                )}
                {c.connector === 'cloud' && (
                  <MenuItem onSelect={() => rbacMutation.mutate(c.id)}>
                    <ShieldCheck className="h-4 w-4" aria-hidden="true" />
                    {t('clusterAdmin.cloudRbac')}
                  </MenuItem>
                )}
                <MenuItem onSelect={() => setEditTarget(c)}>
                  <Pencil className="h-4 w-4" aria-hidden="true" />
                  {t('clusterAdmin.edit')}
                </MenuItem>
                {/* The local cluster cannot be disabled (the API answers 400): it is where the panel runs. */}
                {c.connector !== 'incluster' && (
                  <MenuItem onSelect={() => toggleMutation.mutate({ id: c.id, enabled: !c.enabled })}>
                    <Power className="h-4 w-4" aria-hidden="true" />
                    {c.enabled ? t('userAdmin.disable') : t('userAdmin.enable')}
                  </MenuItem>
                )}
                {c.connector !== 'incluster' && (
                  <MenuItem
                    danger
                    onSelect={() => {
                      setDeleteTarget(c)
                      setConfirmText('')
                      setDeleteError(null)
                    }}
                  >
                    <Trash2 className="h-4 w-4" aria-hidden="true" />
                    {t('clusterAdmin.delete')}
                  </MenuItem>
                )}
              </RowActions>
            )}
          />
          {withErrors.length > 0 && (
            <Card className="p-4 text-sm">
              <h3 className="font-semibold text-danger">{t('clusterAdmin.errorsTitle')}</h3>
              <ul className="mt-2 space-y-1">
                {withErrors.map((c) => (
                  <li key={c.id} className="flex gap-2">
                    <span className="shrink-0 font-mono">{c.displayName}:</span>
                    <span className="break-all text-fg-muted">{c.status.lastError}</span>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>

        <Card className="h-fit min-w-0 p-5">
          <h2 className="text-lg font-semibold">{t('clusterAdmin.addTitle')}</h2>
          <form className="mt-4 space-y-4" onSubmit={handleSubmit}>
            <Field label={t('clusterAdmin.displayName')}>
              <Input
                value={form.displayName ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, displayName: e.target.value }))}
                placeholder="prod-eu, staging, …"
                required
              />
            </Field>
            <Field label={t('clusterAdmin.description')}>
              <Input
                value={form.description ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))}
                maxLength={200}
              />
            </Field>

            <ConnectionFields mode={mode} setMode={setMode} form={form} setForm={setForm} editing={false} />

            {error && <Alert tone="danger">{error}</Alert>}

            <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
              {createMutation.isPending ? t('clusterAdmin.validating') : t('clusterAdmin.submit')}
            </Button>
          </form>
        </Card>
      </div>

      {onboarding && <AgentOnboardingModal onboarding={onboarding} onClose={() => setOnboarding(null)} />}
      {cloudOnboarding && <CloudOnboardingModal onboarding={cloudOnboarding} onClose={() => setCloudOnboarding(null)} />}

      {editTarget && (
        <EditClusterModal
          cluster={editTarget}
          onClose={() => setEditTarget(null)}
          onSaved={() => {
            invalidate()
            setEditTarget(null)
          }}
        />
      )}

      {/* Own modal and not `useConfirm({typeToConfirm})`: if the delete fails the
          error is shown right here and the admin retries without reopening it. */}
      {deleteTarget && (
        <Modal
          onClose={() => setDeleteTarget(null)}
          variant="center"
          size="sm"
          title={t('clusterAdmin.confirmDeleteTitle')}
          footer={
            <>
              <Button variant="secondary" onClick={() => setDeleteTarget(null)} disabled={deleteMutation.isPending}>
                {t('clusterAdmin.cancel')}
              </Button>
              <Button
                variant="danger"
                disabled={confirmText !== deleteTarget.displayName || deleteMutation.isPending}
                onClick={() => deleteMutation.mutate(deleteTarget.id)}
              >
                {t('clusterAdmin.confirmDeleteSubmit')}
              </Button>
            </>
          }
        >
          <p className="text-sm text-fg-muted">{t('clusterAdmin.confirmDeleteBody', { name: deleteTarget.displayName })}</p>
          <Field label={t('clusterAdmin.confirmDeleteLabel')} className="mt-4">
            <Input value={confirmText} onChange={(e) => setConfirmText(e.target.value)} autoFocus />
          </Field>
          {deleteError && (
            <Alert tone="danger" className="mt-2">
              {deleteError}
            </Alert>
          )}
        </Modal>
      )}
    </div>
  )
}

/**
 * Leaves in the payload only the fields of the chosen mode: if the admin filled
 * in the token and then switched to kubeconfig, the token must not travel.
 */
function cleanPayload(form: clustersApi.ClusterPayload, mode: CredentialMode): clustersApi.ClusterPayload {
  const out: clustersApi.ClusterPayload = {}
  if (form.displayName !== undefined) out.displayName = form.displayName
  if (form.description !== undefined) out.description = form.description
  if (mode === 'agent') {
    out.connector = 'agent'
    if (form.kind === 'docker') out.kind = 'docker'
    if (form.insecureSkipTLSVerify) out.insecureSkipTLSVerify = true
    return out
  }
  if (mode === 'cloud') {
    out.connector = 'cloud'
    out.cloudCredentialId = form.cloudCredentialId
    out.cloudRef = form.cloudRef
    return out
  }
  if (mode === 'kubeconfig') {
    if (form.kubeconfig) out.kubeconfig = form.kubeconfig
    return out
  }
  if (form.apiEndpoint) out.apiEndpoint = form.apiEndpoint
  if (form.insecureSkipTLSVerify !== undefined) out.insecureSkipTLSVerify = form.insecureSkipTLSVerify
  if (form.caCert && !form.insecureSkipTLSVerify) out.caCert = form.caCert
  if (mode === 'token' && form.saToken) out.saToken = form.saToken
  if (mode === 'clientCert') {
    if (form.clientCert) out.clientCert = form.clientCert
    if (form.clientKey) out.clientKey = form.clientKey
  }
  return out
}

/**
 * Edit in a drawer. Name and description always; the connection is only touched
 * if the admin fills something in (empty credential fields keep the current
 * one): rotating a token no longer requires deleting the cluster and losing its
 * permissions.
 */
function EditClusterModal({
  cluster,
  onClose,
  onSaved,
}: {
  cluster: clustersApi.Cluster
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const formId = useId()
  const [form, setForm] = useState<clustersApi.ClusterPayload>({
    displayName: cluster.displayName,
    description: cluster.description ?? '',
    apiEndpoint: cluster.apiEndpoint,
    insecureSkipTLSVerify: cluster.insecureSkipTLSVerify,
  })
  const [mode, setMode] = useState<CredentialMode>(cluster.connector === 'direct' && cluster.authType !== 'incluster' ? cluster.authType : 'kubeconfig')
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: (payload: clustersApi.ClusterPayload) => clustersApi.updateCluster(cluster.id, payload),
    onSuccess: onSaved,
    onError: (err) => setError(apiErrorText(err, t('clusterAdmin.error'))),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    // Agent and cloud have no credentials of their own to edit.
    const payload = cluster.connector !== 'direct' ? { displayName: form.displayName, description: form.description } : cleanPayload(form, mode)
    // An endpoint equal to the current one is not a connection change: avoids
    // revalidating (and asking for a CA) when only renaming.
    if (payload.apiEndpoint === cluster.apiEndpoint && !payload.caCert && !payload.saToken && !payload.clientKey && !payload.kubeconfig) {
      delete payload.apiEndpoint
      if (payload.insecureSkipTLSVerify === cluster.insecureSkipTLSVerify) delete payload.insecureSkipTLSVerify
    }
    mutation.mutate(payload)
  }

  return (
    <Modal
      onClose={onClose}
      size="md"
      title={t('clusterAdmin.editTitle', { name: cluster.displayName })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={mutation.isPending}>
            {mutation.isPending ? t('clusterAdmin.validating') : t('clusterAdmin.save')}
          </Button>
        </>
      }
    >
      <form id={formId} onSubmit={handleSubmit} className="space-y-4">
        <Field label={t('clusterAdmin.displayName')}>
          <Input
            value={form.displayName ?? ''}
            onChange={(e) => setForm((f) => ({ ...f, displayName: e.target.value }))}
            required
          />
        </Field>
        <Field label={t('clusterAdmin.description')}>
          <Input
            value={form.description ?? ''}
            onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))}
            maxLength={200}
          />
        </Field>

        {cluster.connector === 'direct' && (
          <div className="rounded-md border border-border p-4">
            <h3 className="flex items-center gap-2 text-sm font-semibold">
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
              {t('clusterAdmin.connectionTitle')}
            </h3>
            <p className="mt-1 mb-3 text-xs text-fg-muted">{t('clusterAdmin.connectionHint')}</p>
            <div className="space-y-3">
              <ConnectionFields mode={mode} setMode={setMode} form={form} setForm={setForm} editing />
            </div>
          </div>
        )}

        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}


/**
 * After creating (or regenerating) an agent cluster: the command to run against
 * the cluster. The manifest URL expires in an hour; afterwards it is
 * regenerated from the table without touching the agent key.
 */
function AgentOnboardingModal({ onboarding, onClose }: { onboarding: clustersApi.AgentOnboarding; onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const { copy, copied } = useCopy()
  return (
    <Modal
      onClose={onClose}
      variant="center"
      size="lg"
      title={t('clusterAdmin.onboardingTitle', { name: onboarding.cluster.displayName })}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      <p className="text-sm text-fg-muted">
        {onboarding.cluster.kind === 'docker' ? t('clusterAdmin.onboardingDockerHint') : t('clusterAdmin.onboardingHint')}
      </p>
      <div className="mt-3 flex items-start gap-2">
        <code className="flex-1 overflow-x-auto rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs whitespace-pre">{onboarding.command}</code>
        <Button size="sm" onClick={() => void copy(onboarding.command)}>
          <Copy className="h-3.5 w-3.5" aria-hidden="true" />
          {copied ? t('common.copied') : t('common.copy')}
        </Button>
      </div>
      {onboarding.cluster.kind !== 'docker' && (
        <p className="mt-2 text-xs text-fg-muted">
          {t('clusterAdmin.onboardingExpires', { when: formatTime(onboarding.expiresAt, i18n.resolvedLanguage) })}
        </p>
      )}
    </Modal>
  )
}

/**
 * After creating a cloud cluster (or from the table's RBAC button): the YAML
 * that grants the provider identity the panel's permissions, with the
 * provider-specific previous steps (on EKS the IAM identity has to be mapped
 * to a group before the RBAC means anything).
 */
function CloudOnboardingModal({ onboarding, onClose }: { onboarding: clustersApi.CloudOnboarding; onClose: () => void }) {
  const { t } = useTranslation()
  const { copy, copied } = useCopy()
  const ref = onboarding.cluster.cloud?.ref
  function download() {
    const blob = new Blob([onboarding.rbacYaml], { type: 'application/yaml' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `bayplan-rbac-${onboarding.cluster.displayName}.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <Modal
      onClose={onClose}
      variant="center"
      size="lg"
      title={t('clusterAdmin.cloudOnboardingTitle', { name: onboarding.cluster.displayName })}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      <p className="text-sm text-fg-muted">{t('clusterAdmin.cloudOnboardingHint')}</p>
      <ol className="mt-3 list-decimal space-y-2 pl-5 text-sm">
        {onboarding.provider === 'aws' && (
          <li>
            {t('clusterAdmin.cloudStepAws', { identity: onboarding.identity, group: onboarding.subjectName })}
            <code className="mt-1 block overflow-x-auto rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs whitespace-pre">
              {`aws eks create-access-entry --region ${ref?.region ?? '<region>'} --cluster-name ${ref?.name ?? '<cluster>'} \\\n  --principal-arn ${onboarding.identity} --kubernetes-groups ${onboarding.subjectName}`}
            </code>
          </li>
        )}
        {onboarding.provider === 'azure' && <li>{t('clusterAdmin.cloudStepAzure', { identity: onboarding.identity })}</li>}
        {onboarding.provider === 'gcp' && <li>{t('clusterAdmin.cloudStepGcp', { identity: onboarding.identity })}</li>}
        <li>{t('clusterAdmin.cloudStepApply')}</li>
      </ol>
      <div className="mt-3 flex items-start gap-2">
        <pre className="max-h-64 flex-1 overflow-auto rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs">{onboarding.rbacYaml}</pre>
        <div className="flex flex-col gap-1">
          <Button size="sm" onClick={() => void copy(onboarding.rbacYaml)}>
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
            {copied ? t('common.copied') : t('common.copy')}
          </Button>
          <Button size="sm" onClick={download}>
            <Download className="h-3.5 w-3.5" aria-hidden="true" />
            YAML
          </Button>
        </div>
      </div>
    </Modal>
  )
}
