import { useMemo, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw, Trash2, X } from 'lucide-react'
import * as registriesApi from '../api/registries'
import * as resources from '../api/resources'
import { listClusters } from '../api/clusters'
import { useAuth } from '../hooks/useAuth'
import { SearchInput } from '../components/SearchInput'
import { Age, Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, PageHeader, RowActions, Select, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { NotFound } from './NotFound'
import { apiErrorText } from '../lib/errors'

/**
 * Registries bound to this cluster: each row is a dockerconfigjson Secret
 * in a namespace (and, if checked, in the imagePullSecrets of the `default` SA).
 * Creating the binding syncs it right away; the cluster's error is shown
 * in the row and can be retried.
 */
export function ClusterRegistries() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0

  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const canWrite = cluster?.role === 'admin' || cluster?.role === 'editor'
  const isGlobalAdmin = user?.role === 'admin'

  const bindingsQuery = useQuery({ queryKey: ['registry-bindings', clusterId], queryFn: () => registriesApi.listBindings(clusterId), enabled: validCluster, refetchInterval: 60_000 })
  const registriesQuery = useQuery({ queryKey: ['registries'], queryFn: registriesApi.listRegistries, enabled: canWrite })
  const namespacesQuery = useQuery({ queryKey: ['namespaces', clusterId], queryFn: () => resources.listNamespaces(clusterId), enabled: validCluster && canWrite })
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['registry-bindings', clusterId] })
    void queryClient.invalidateQueries({ queryKey: ['registries'] })
  }
  const [tableState, setTableState] = useTableState({ sortKey: 'registry' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const query = filters.q.trim().toLowerCase()
  const filtered = useMemo(() => {
    const items = bindingsQuery.data ?? []
    if (!query) return items
    return items.filter((b) => b.registryName.toLowerCase().includes(query) || b.server.toLowerCase().includes(query) || b.namespace.toLowerCase().includes(query))
  }, [bindingsQuery.data, query])
  const [form, setForm] = useState({ registryId: '', namespace: '', attachDefaultSA: true })
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: () => registriesApi.createBinding(clusterId, { registryId: Number(form.registryId), namespace: form.namespace.trim(), attachDefaultSA: form.attachDefaultSA }),
    onSuccess: (b) => {
      invalidate()
      setForm((f) => ({ ...f, namespace: '' }))
      setError(b.lastError ? t('registries.bindSyncFailed', { error: b.lastError }) : null)
    },
    onError: (err) => setError(apiErrorText(err, t('registries.error'))),
  })
  const syncMutation = useMutation({
    mutationFn: (bid: number) => registriesApi.syncBinding(clusterId, bid),
    onSuccess: () => {
      invalidate()
      setError(null)
    },
    onError: (err) => {
      invalidate()
      setError(apiErrorText(err, t('registries.error')))
    },
  })
  const deleteMutation = useMutation({
    mutationFn: (bid: number) => registriesApi.deleteBinding(clusterId, bid),
    onSuccess: (res) => {
      invalidate()
      setNotice(res && typeof res === 'object' && res.warning ? res.warning : null)
    },
    onError: (err) => setError(apiErrorText(err, t('registries.error'))),
  })

  if (!validCluster || (clustersQuery.data && !cluster)) return <NotFound />

  const columns: DataTableColumn<registriesApi.RegistryBinding>[] = [
    {
      key: 'registry',
      header: t('registries.registry'),
      primary: true,
      render: (b) => (
        <div className="min-w-0">
          <div className="font-mono">{b.registryName}</div>
          <div className="truncate text-xs text-fg-muted">{b.server}</div>
        </div>
      ),
      sortValue: (b) => b.registryName,
    },
    { key: 'namespace', header: t('cluster.columns.namespace'), primary: true, cellClassName: 'font-mono', render: (b) => b.namespace, sortValue: (b) => b.namespace },
    {
      key: 'secret',
      header: t('registries.secret'),
      cellClassName: 'text-xs',
      render: (b) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="font-mono">{b.secretName}</span>
          {b.attachDefaultSA && <Badge tone="neutral">{t('registries.defaultSA')}</Badge>}
        </span>
      ),
    },
    {
      key: 'status',
      header: t('cluster.columns.status'),
      primary: true,
      render: (b) =>
        b.lastError ? (
          <span className="text-xs break-words text-danger" title={b.lastError}>
            {b.lastError}
          </span>
        ) : b.lastSyncedAt ? (
          <span className="flex items-center gap-1 text-xs">
            <Badge tone="success">{t('registries.synced')}</Badge>
            <Age createdAt={b.lastSyncedAt} />
          </span>
        ) : (
          <Badge tone="warning">{t('registries.pending')}</Badge>
        ),
      sortValue: (b) => (b.lastError ? 0 : 1),
    },
  ]

  const rowActions = (b: registriesApi.RegistryBinding) =>
    canWrite ? (
      <RowActions>
        <MenuItem onSelect={() => syncMutation.mutate(b.id)} disabled={syncMutation.isPending}>
          <RefreshCw className="h-4 w-4 shrink-0" aria-hidden="true" />
          {t('registries.sync')}
        </MenuItem>
        <MenuItem
          danger
          onSelect={async () => {
            if (!(await confirm({ title: t('registries.confirmUnbind', { name: b.registryName, namespace: b.namespace }), danger: true }))) return
            deleteMutation.mutate(b.id)
          }}
        >
          <Trash2 className="h-4 w-4 shrink-0" aria-hidden="true" />
          {t('clusterAdmin.delete')}
        </MenuItem>
      </RowActions>
    ) : null

  return (
    <div>
      <PageHeader
        title={t('registries.clusterTitle')}
        description={t('registries.clusterHint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('registries.clusterTitle') }]}
      />
      {error && (
        <Alert tone="danger" className="mb-4">
          {error}
        </Alert>
      )}
      {notice && (
        <Alert tone="warning" className="mb-4">
          {notice}
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
            rowKey={(b) => String(b.id)}
            filtered={query !== ''}
            isLoading={bindingsQuery.isPending}
            error={bindingsQuery.isError ? apiErrorText(bindingsQuery.error, t('registries.error')) : undefined}
            emptyMessage={t('registries.noBindings')}
            actions={rowActions}
            state={tableState}
            onStateChange={setTableState}
          />
        </div>
        {canWrite && (
          <Card className="h-fit min-w-0 p-5">
            <h2 className="text-sm font-semibold">{t('registries.bind')}</h2>
            {registriesQuery.data && registriesQuery.data.length === 0 ? (
              <p className="mt-2 text-xs text-fg-muted">
                {t('registries.noRegistries')}{' '}
                {isGlobalAdmin && (
                  <Link to="/admin/registries" className="underline">
                    {t('registries.title')}
                  </Link>
                )}
              </p>
            ) : (
              <form
                onSubmit={(e: FormEvent) => {
                  e.preventDefault()
                  setError(null)
                  setNotice(null)
                  createMutation.mutate()
                }}
                className="mt-3 space-y-3"
              >
                <Field label={t('registries.registry')}>
                  <Select value={form.registryId} onChange={(e) => setForm((f) => ({ ...f, registryId: e.target.value }))} required>
                    <option value="">—</option>
                    {(registriesQuery.data ?? []).map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.name} · {r.server}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label={t('cluster.columns.namespace')}>
                  <Input mono list="registry-namespaces" value={form.namespace} onChange={(e) => setForm((f) => ({ ...f, namespace: e.target.value }))} required pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?" />
                </Field>
                <datalist id="registry-namespaces">
                  {(namespacesQuery.data ?? []).map((ns) => (
                    <option key={ns.name} value={ns.name} />
                  ))}
                </datalist>
                <Checkbox
                  label={t('registries.attachDefaultSA')}
                  hint={t('registries.attachDefaultSAHint')}
                  checked={form.attachDefaultSA}
                  onChange={(e) => setForm((f) => ({ ...f, attachDefaultSA: e.target.checked }))}
                />
                <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
                  {createMutation.isPending ? t('registries.syncing') : t('registries.bind')}
                </Button>
              </form>
            )}
          </Card>
        )}
      </div>
    </div>
  )
}
