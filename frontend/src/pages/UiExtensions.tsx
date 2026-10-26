import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as extApi from '../api/extensions'
import { listClusters } from '../api/clusters'
import { Pencil, Trash2 } from 'lucide-react'
import { Alert, Badge, Button, Card, DataTable, Field, Input, MenuItem, PageHeader, RowActions, Select, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'

const EMPTY: extApi.UiExtensionInput = { clusterId: null, name: '', url: '', mode: 'link', minRole: 'readonly', sortOrder: 0 }


/**
 * Links to external tools per cluster (global admin). The same
 * form creates and edits: picking a row fills it in; "New" clears it.
 */
export function UiExtensions() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const extQuery = useQuery({ queryKey: ['extensions'], queryFn: extApi.listExtensions })
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const [tableState, setTableState] = useTableState({ sortKey: 'sortOrder' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [editing, setEditing] = useState<number | null>(null)
  const [form, setForm] = useState<extApi.UiExtensionInput>(EMPTY)
  const [error, setError] = useState<string | null>(null)

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['extensions'] })
    // The per-cluster lists (sidebar) are cached separately.
    void queryClient.invalidateQueries({ queryKey: ['cluster-extensions'] })
  }
  const reset = () => {
    setEditing(null)
    setForm(EMPTY)
    setError(null)
  }

  const saveMutation = useMutation({
    mutationFn: (payload: extApi.UiExtensionInput) => (editing === null ? extApi.createExtension(payload) : extApi.updateExtension(editing, payload)),
    onSuccess: () => {
      invalidate()
      reset()
    },
    onError: (err) => setError(apiErrorText(err, t('extensions.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: extApi.deleteExtension,
    onSuccess: () => {
      invalidate()
      reset()
    },
    onError: (err) => setError(apiErrorText(err, t('extensions.error'))),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate(form)
  }

  const clusterName = (id?: number) => (id === undefined ? t('extensions.allClusters') : (clustersQuery.data?.find((c) => c.id === id)?.displayName ?? `#${id}`))

  const search = filters.q.trim().toLowerCase()
  const extensions = (extQuery.data ?? []).filter(
    (r) => !search || r.name.toLowerCase().includes(search) || r.url.toLowerCase().includes(search) || clusterName(r.clusterId).toLowerCase().includes(search),
  )

  const columns: DataTableColumn<extApi.UiExtension>[] = [
    { key: 'name', header: t('cluster.columns.name'), cellClassName: 'font-medium', render: (r) => r.name, sortValue: (r) => r.name, primary: true },
    { key: 'cluster', header: t('cluster.title'), render: (r) => clusterName(r.clusterId), sortValue: (r) => clusterName(r.clusterId), primary: true },
    {
      key: 'url',
      header: 'URL',
      cellClassName: 'max-w-0 w-full font-mono text-xs text-fg-muted',
      render: (r) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="min-w-0 max-w-full truncate" title={r.url}>
            {r.url}
          </span>
          <Badge tone="neutral">{t(`extensions.mode.${r.mode}`)}</Badge>
          {r.minRole !== 'readonly' && <Badge tone="warning">{r.minRole}</Badge>}
        </span>
      ),
      sortValue: (r) => r.url,
    },
    { key: 'sortOrder', header: t('extensions.order'), render: (r) => r.sortOrder, sortValue: (r) => r.sortOrder },
  ]

  return (
    <div>
      <PageHeader
        title={t('extensions.title')}
        description={t('extensions.description')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('extensions.title') }]}
      />
      {error && (
        <Alert tone="danger" className="mb-4">
          {error}
        </Alert>
      )}
      <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
        <div className="min-w-0">
          <Toolbar>
            <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v })} />
            {search && (
              <Button variant="ghost" size="sm" onClick={() => setFilters({ q: '' })}>
                {t('common.clearFilters')}
              </Button>
            )}
          </Toolbar>
          <DataTable
            columns={columns}
            data={extensions}
            rowKey={(r) => String(r.id)}
            isLoading={extQuery.isPending}
            error={extQuery.isError ? apiErrorText(extQuery.error, t('common.error')) : undefined}
            filtered={search !== ''}
            emptyMessage={t('extensions.empty')}
            state={tableState}
            onStateChange={setTableState}
            actions={(r) => (
              <RowActions>
                <MenuItem
                  onSelect={() => {
                    setEditing(r.id)
                    setForm({ clusterId: r.clusterId ?? null, name: r.name, url: r.url, mode: r.mode, minRole: r.minRole, sortOrder: r.sortOrder })
                    setError(null)
                  }}
                >
                  <Pencil className="h-4 w-4" aria-hidden="true" />
                  {t('clusterAdmin.edit')}
                </MenuItem>
                <MenuItem
                  danger
                  onSelect={async () => {
                    if (!(await confirm({ title: t('extensions.confirmDelete', { name: r.name }), danger: true, confirmLabel: t('clusterAdmin.delete') }))) return
                    deleteMutation.mutate(r.id)
                  }}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                  {t('clusterAdmin.delete')}
                </MenuItem>
              </RowActions>
            )}
          />
        </div>
        <Card className="h-fit min-w-0 p-5">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold">{editing === null ? t('extensions.add') : t('extensions.editTitle')}</h2>
            {editing !== null && (
              <Button size="sm" onClick={reset}>
                {t('extensions.new')}
              </Button>
            )}
          </div>
          <form onSubmit={handleSubmit} className="mt-3 space-y-3">
            <Field label={t('cluster.columns.name')}>
              <Input value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} required maxLength={60} placeholder="Grafana" />
            </Field>
            <Field label="URL" hint={t('extensions.urlHint')}>
              <Input mono value={form.url} onChange={(e) => setForm((f) => ({ ...f, url: e.target.value }))} required placeholder="https://grafana.example.com/d/k8s?var-cluster={cluster}&var-namespace={namespace}" />
            </Field>
            <Field label={t('cluster.title')}>
              <Select value={form.clusterId ?? ''} onChange={(e) => setForm((f) => ({ ...f, clusterId: e.target.value === '' ? null : Number(e.target.value) }))}>
                <option value="">{t('extensions.allClusters')}</option>
                {(clustersQuery.data ?? [])
                  .filter((c) => c.kind !== 'docker')
                  .map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.displayName}
                    </option>
                  ))}
              </Select>
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('extensions.modeLabel')}>
                <Select value={form.mode} onChange={(e) => setForm((f) => ({ ...f, mode: e.target.value as extApi.ExtensionMode }))}>
                  <option value="link">{t('extensions.mode.link')}</option>
                  <option value="embed">{t('extensions.mode.embed')}</option>
                </Select>
              </Field>
              <Field label={t('extensions.minRole')}>
                <Select value={form.minRole} onChange={(e) => setForm((f) => ({ ...f, minRole: e.target.value as extApi.ExtensionRole }))}>
                  <option value="readonly">readonly</option>
                  <option value="editor">editor</option>
                  <option value="admin">admin</option>
                </Select>
              </Field>
            </div>
            <Field label={t('extensions.order')}>
              <Input type="number" value={form.sortOrder} min={-1000} max={1000} onChange={(e) => setForm((f) => ({ ...f, sortOrder: Number(e.target.value) || 0 }))} />
            </Field>
            {form.mode === 'embed' && <p className="text-xs text-fg-muted">{t('extensions.embedHint')}</p>}
            <Button type="submit" variant="primary" disabled={saveMutation.isPending} className="w-full">
              {editing === null ? t('extensions.add') : t('clusterAdmin.save')}
            </Button>
          </form>
        </Card>
      </div>
    </div>
  )
}
