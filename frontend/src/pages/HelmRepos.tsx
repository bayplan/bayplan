import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Trash2 } from 'lucide-react'
import * as helmApi from '../api/helm'
import { Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, PageHeader, RowActions, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'

/**
 * Chart repositories (platform, global admin). Adding one downloads the index
 * before saving: a mistyped URL is rejected here, it does not show up empty in
 * the catalog.
 */
export function HelmRepos() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const reposQuery = useQuery({ queryKey: ['helm-repos'], queryFn: helmApi.listHelmRepos })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [form, setForm] = useState<helmApi.CreateHelmRepoPayload>({ name: '', url: '', username: '', password: '', insecureSkipTLS: false })
  const [error, setError] = useState<string | null>(null)
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['helm-repos'] })

  const createMutation = useMutation({
    mutationFn: helmApi.createHelmRepo,
    onSuccess: () => {
      invalidate()
      setForm({ name: '', url: '', username: '', password: '', insecureSkipTLS: false })
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('helm.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: helmApi.deleteHelmRepo,
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('helm.error'))),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate({ ...form, username: form.username || undefined, password: form.password || undefined })
  }

  const search = filters.q.trim().toLowerCase()
  const repos = (reposQuery.data ?? []).filter((r) => !search || r.name.toLowerCase().includes(search) || r.url.toLowerCase().includes(search))

  const columns: DataTableColumn<helmApi.HelmRepo>[] = [
    { key: 'name', header: t('cluster.columns.name'), cellClassName: 'font-medium', render: (r) => r.name, sortValue: (r) => r.name, primary: true },
    {
      key: 'url',
      header: 'URL',
      // max-w-0 + w-full: the column takes the leftover width and the URL gets
      // truncated; without it a long URL sets the table's minimum width and pushes
      // it out of its grid column.
      cellClassName: 'max-w-0 w-full font-mono text-xs text-fg-muted',
      render: (r) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="min-w-0 max-w-full truncate" title={r.url}>
            {r.url}
          </span>
          <Badge tone="neutral">{r.type.toUpperCase()}</Badge>
          {r.hasPassword && <Badge tone="neutral">{t('helm.authenticated')}</Badge>}
          {r.insecureSkipTLS && <Badge tone="warning">{t('clusterAdmin.insecureBadge')}</Badge>}
        </span>
      ),
      sortValue: (r) => r.url,
      primary: true,
    },
  ]

  return (
    <div>
      <PageHeader
        title={t('helm.reposTitle')}
        description={t('helm.reposHint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('helm.reposTitle') }]}
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
            data={repos}
            rowKey={(r) => String(r.id)}
            isLoading={reposQuery.isPending}
            error={reposQuery.isError ? apiErrorText(reposQuery.error, t('common.error')) : undefined}
            filtered={search !== ''}
            emptyMessage={t('helm.noRepos')}
            state={tableState}
            onStateChange={setTableState}
            actions={(r) => (
              <RowActions>
                <MenuItem
                  danger
                  onSelect={async () => {
                    if (!(await confirm({ title: t('helm.confirmDeleteRepo', { name: r.name }), danger: true, confirmLabel: t('clusterAdmin.delete') }))) return
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
          <h2 className="text-sm font-semibold">{t('helm.addRepo')}</h2>
          <form onSubmit={handleSubmit} className="mt-3 space-y-3">
            <Field label={t('cluster.columns.name')}>
              <Input mono value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} required pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?" placeholder="bitnami" />
            </Field>
            <Field label="URL">
              <Input mono value={form.url} onChange={(e) => setForm((f) => ({ ...f, url: e.target.value }))} required placeholder="https://charts.bitnami.com/bitnami  |  oci://registry.example/charts" />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('login.username')}>
                <Input value={form.username ?? ''} onChange={(e) => setForm((f) => ({ ...f, username: e.target.value }))} autoComplete="off" />
              </Field>
              <Field label={t('login.password')}>
                <Input type="password" value={form.password ?? ''} onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))} autoComplete="new-password" />
              </Field>
            </div>
            <Checkbox label={t('clusterAdmin.insecure')} checked={form.insecureSkipTLS ?? false} onChange={(e) => setForm((f) => ({ ...f, insecureSkipTLS: e.target.checked }))} />
            <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
              {createMutation.isPending ? t('helm.checkingRepo') : t('helm.addRepo')}
            </Button>
          </form>
        </Card>
      </div>
    </div>
  )
}
