import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Trash2 } from 'lucide-react'
import * as registriesApi from '../api/registries'
import { Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, Modal, PageHeader, RowActions, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'

interface RegistryForm {
  name: string
  server: string
  username: string
  password: string
  email: string
  insecureSkipTLS: boolean
}
const EMPTY: RegistryForm = { name: '', server: '', username: '', password: '', email: '', insecureSkipTLS: false }

function toPayload(f: RegistryForm, editing: boolean): registriesApi.RegistryPayload {
  const out: registriesApi.RegistryPayload = { server: f.server.trim(), username: f.username.trim(), email: f.email.trim(), insecureSkipTLS: f.insecureSkipTLS }
  if (!editing) out.name = f.name.trim()
  if (f.password) out.password = f.password
  return out
}

function RegistryFields({ form, setForm, editing }: { form: RegistryForm; setForm: (fn: (f: RegistryForm) => RegistryForm) => void; editing: boolean }) {
  const { t } = useTranslation()
  const set = <K extends keyof RegistryForm>(k: K, v: RegistryForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  return (
    <>
      <Field label={t('cluster.columns.name')} hint={t('registries.nameHint')}>
        <Input mono value={form.name} onChange={(e) => set('name', e.target.value)} required disabled={editing} pattern="[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?" placeholder="ghcr-corp" />
      </Field>
      <Field label={t('registries.server')}>
        <Input mono value={form.server} onChange={(e) => set('server', e.target.value)} required placeholder="ghcr.io · docker.io · harbor.example.com" />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('login.username')}>
          <Input value={form.username} onChange={(e) => set('username', e.target.value)} required autoComplete="off" />
        </Field>
        <Field label={t('registries.password')}>
          <Input type="password" value={form.password} onChange={(e) => set('password', e.target.value)} required={!editing} autoComplete="new-password" placeholder={editing ? t('clusterAdmin.keepCredential') : ''} />
        </Field>
      </div>
      <Field label={t('registries.email')}>
        <Input type="email" value={form.email} onChange={(e) => set('email', e.target.value)} />
      </Field>
      <Checkbox label={t('registries.insecure')} checked={form.insecureSkipTLS} onChange={(e) => set('insecureSkipTLS', e.target.checked)} />
    </>
  )
}

/**
 * Image registries (platform, global admin). Creating one checks the
 * credential against the registry (Distribution API) before saving it; the
 * distribution to namespaces is done from each cluster (Registries tab).
 */
export function ImageRegistries() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['registries'] })
  const query = useQuery({ queryKey: ['registries'], queryFn: registriesApi.listRegistries })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [form, setForm] = useState<RegistryForm>(EMPTY)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [editTarget, setEditTarget] = useState<registriesApi.ImageRegistry | null>(null)

  const createMutation = useMutation({
    mutationFn: registriesApi.createRegistry,
    onSuccess: () => {
      invalidate()
      setForm(EMPTY)
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('registries.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: registriesApi.deleteRegistry,
    onSuccess: (res) => {
      invalidate()
      setNotice(t('registries.deleted', { removed: res.secretsRemoved, failed: res.secretsFailed }))
    },
    onError: (err) => setError(apiErrorText(err, t('registries.error'))),
  })

  const search = filters.q.trim().toLowerCase()
  const registries = (query.data ?? []).filter(
    (r) => !search || r.name.toLowerCase().includes(search) || r.server.toLowerCase().includes(search) || r.username.toLowerCase().includes(search),
  )

  const columns: DataTableColumn<registriesApi.ImageRegistry>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      render: (r) => (
        <div className="min-w-0">
          <div className="font-medium">{r.name}</div>
          <div className="truncate font-mono text-xs text-fg-muted">{r.secretName}</div>
        </div>
      ),
      sortValue: (r) => r.name,
      primary: true,
    },
    {
      key: 'server',
      header: t('registries.server'),
      cellClassName: 'max-w-0 w-full font-mono text-xs',
      render: (r) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="min-w-0 max-w-full truncate" title={r.server}>
            {r.server}
          </span>
          <span className="text-fg-muted">({r.username})</span>
          {r.insecureSkipTLS && <Badge tone="warning">{t('clusterAdmin.insecureBadge')}</Badge>}
        </span>
      ),
      sortValue: (r) => r.server,
      primary: true,
    },
    { key: 'bindings', header: t('registries.bindings'), render: (r) => r.bindingCount, sortValue: (r) => r.bindingCount },
  ]

  return (
    <div>
      <PageHeader
        title={t('registries.title')}
        description={t('registries.description')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('registries.title') }]}
      />
      {error && (
        <Alert tone="danger" className="mb-4">
          {error}
        </Alert>
      )}
      {notice && (
        <Alert tone="success" className="mb-4">
          {notice}
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
            data={registries}
            rowKey={(r) => String(r.id)}
            isLoading={query.isPending}
            error={query.isError ? apiErrorText(query.error, t('common.error')) : undefined}
            filtered={search !== ''}
            emptyMessage={t('registries.empty')}
            state={tableState}
            onStateChange={setTableState}
            actions={(r) => (
              <RowActions>
                <MenuItem onSelect={() => setEditTarget(r)}>
                  <Pencil className="h-4 w-4" aria-hidden="true" />
                  {t('clusterAdmin.edit')}
                </MenuItem>
                <MenuItem
                  danger
                  onSelect={async () => {
                    if (!(await confirm({ title: t('registries.confirmDelete', { name: r.name, count: r.bindingCount }), danger: true, confirmLabel: t('clusterAdmin.delete') }))) return
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
          <h2 className="text-sm font-semibold">{t('registries.add')}</h2>
          <p className="mt-1 text-xs text-fg-muted">{t('registries.addHint')}</p>
          <form
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              setError(null)
              setNotice(null)
              createMutation.mutate(toPayload(form, false))
            }}
            className="mt-3 space-y-3"
          >
            <RegistryFields form={form} setForm={setForm} editing={false} />
            <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
              {createMutation.isPending ? t('registries.validating') : t('registries.add')}
            </Button>
          </form>
        </Card>
      </div>
      {editTarget && (
        <EditRegistryModal
          registry={editTarget}
          onClose={() => setEditTarget(null)}
          onSaved={() => {
            invalidate()
            setEditTarget(null)
          }}
        />
      )}
    </div>
  )
}

function EditRegistryModal({ registry, onClose, onSaved }: { registry: registriesApi.ImageRegistry; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation()
  const formId = useId()
  const [form, setForm] = useState<RegistryForm>({ ...EMPTY, name: registry.name, server: registry.server, username: registry.username, email: registry.email ?? '', insecureSkipTLS: registry.insecureSkipTLS })
  const [error, setError] = useState<string | null>(null)
  const mutation = useMutation({
    mutationFn: (payload: registriesApi.RegistryPayload) => registriesApi.updateRegistry(registry.id, payload),
    onSuccess: onSaved,
    onError: (err) => setError(apiErrorText(err, t('registries.error'))),
  })
  return (
    <Modal
      onClose={onClose}
      size="md"
      title={t('clusterAdmin.editTitle', { name: registry.name })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={mutation.isPending}>
            {mutation.isPending ? t('registries.validating') : t('clusterAdmin.save')}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(e: FormEvent) => {
          e.preventDefault()
          setError(null)
          mutation.mutate(toPayload(form, true))
        }}
        className="space-y-3"
      >
        <RegistryFields form={form} setForm={setForm} editing />
        <p className="text-xs text-fg-muted">{t('registries.rotateHint', { count: registry.bindingCount })}</p>
        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}
