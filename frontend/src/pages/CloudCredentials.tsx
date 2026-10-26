import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Trash2 } from 'lucide-react'
import * as cloudApi from '../api/cloud'
import { Alert, Badge, Button, Card, DataTable, Field, Input, MenuItem, Modal, PageHeader, RowActions, SegmentedControl, Textarea, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'

const PROVIDERS: cloudApi.CloudProvider[] = ['aws', 'azure', 'gcp']


/** Form state: the region list is edited as comma-separated text. */
interface CredentialForm {
  name: string
  provider: cloudApi.CloudProvider
  accessKeyId: string
  regions: string
  tenantId: string
  clientId: string
  subscriptionId: string
  projectId: string
  secretAccessKey: string
  clientSecret: string
  serviceAccountJson: string
}

const EMPTY_FORM: CredentialForm = {
  name: '', provider: 'aws', accessKeyId: '', regions: '', tenantId: '', clientId: '', subscriptionId: '', projectId: '',
  secretAccessKey: '', clientSecret: '', serviceAccountJson: '',
}

function fromCredential(c: cloudApi.CloudCredential): CredentialForm {
  return {
    ...EMPTY_FORM,
    name: c.name,
    provider: c.provider,
    accessKeyId: c.config.accessKeyId ?? '',
    regions: (c.config.regions ?? []).join(', '),
    tenantId: c.config.tenantId ?? '',
    clientId: c.config.clientId ?? '',
    subscriptionId: c.config.subscriptionId ?? '',
    projectId: c.config.projectId ?? '',
  }
}

/** Keeps in the payload only what belongs to the chosen provider; an empty secret does not travel (editing = keep). */
function toPayload(f: CredentialForm, editing: boolean): cloudApi.CloudCredentialPayload {
  const config: cloudApi.CloudConfig = {}
  const secret: cloudApi.CloudSecret = {}
  if (f.provider === 'aws') {
    config.accessKeyId = f.accessKeyId.trim()
    config.regions = f.regions.split(/[,\s]+/).map((r) => r.trim()).filter(Boolean)
    if (f.secretAccessKey) secret.secretAccessKey = f.secretAccessKey.trim()
  } else if (f.provider === 'azure') {
    config.tenantId = f.tenantId.trim()
    config.clientId = f.clientId.trim()
    config.subscriptionId = f.subscriptionId.trim()
    if (f.clientSecret) secret.clientSecret = f.clientSecret.trim()
  } else {
    if (f.projectId.trim()) config.projectId = f.projectId.trim()
    if (f.serviceAccountJson) secret.serviceAccountJson = f.serviceAccountJson.trim()
  }
  const out: cloudApi.CloudCredentialPayload = { name: f.name.trim(), config }
  if (!editing) out.provider = f.provider
  if (Object.keys(secret).length > 0 || !editing) out.secret = secret
  return out
}

/** Credential fields, shared by create and edit. */
function CredentialFields({ form, setForm, editing }: { form: CredentialForm; setForm: (fn: (f: CredentialForm) => CredentialForm) => void; editing: boolean }) {
  const { t } = useTranslation()
  const set = <K extends keyof CredentialForm>(key: K, value: CredentialForm[K]) => setForm((f) => ({ ...f, [key]: value }))
  const keep = editing ? t('clusterAdmin.keepCredential') : ''
  return (
    <>
      {!editing && (
        <SegmentedControl
          size="sm"
          options={PROVIDERS.map((p) => ({ value: p, label: t(`cloud.provider.${p}`) }))}
          value={form.provider}
          onChange={(p) => set('provider', p)}
          className="flex w-full [&>button]:flex-1"
        />
      )}
      <p className="text-xs text-fg-muted">{t(`cloud.hint.${form.provider}`)}</p>
      <Field label={t('cluster.columns.name')}>
        <Input value={form.name} onChange={(e) => set('name', e.target.value)} required placeholder={t(`cloud.namePlaceholder.${form.provider}`)} />
      </Field>
      {form.provider === 'aws' && (
        <>
          <Field label={t('cloud.fields.accessKeyId')}>
            <Input mono value={form.accessKeyId} onChange={(e) => set('accessKeyId', e.target.value)} required autoComplete="off" placeholder="AKIA…" />
          </Field>
          <Field label={t('cloud.fields.secretAccessKey')}>
            <Input mono type="password" value={form.secretAccessKey} onChange={(e) => set('secretAccessKey', e.target.value)} required={!editing} autoComplete="new-password" placeholder={keep} />
          </Field>
          <Field label={t('cloud.regions')} hint={t('cloud.regionsHint')}>
            <Input mono value={form.regions} onChange={(e) => set('regions', e.target.value)} required placeholder="eu-west-1, eu-central-1" />
          </Field>
        </>
      )}
      {form.provider === 'azure' && (
        <>
          <Field label={t('cloud.fields.tenantId')}>
            <Input mono value={form.tenantId} onChange={(e) => set('tenantId', e.target.value)} required autoComplete="off" />
          </Field>
          <Field label={t('cloud.fields.clientId')}>
            <Input mono value={form.clientId} onChange={(e) => set('clientId', e.target.value)} required autoComplete="off" />
          </Field>
          <Field label={t('cloud.fields.clientSecret')}>
            <Input mono type="password" value={form.clientSecret} onChange={(e) => set('clientSecret', e.target.value)} required={!editing} autoComplete="new-password" placeholder={keep} />
          </Field>
          <Field label={t('cloud.fields.subscriptionId')}>
            <Input mono value={form.subscriptionId} onChange={(e) => set('subscriptionId', e.target.value)} required autoComplete="off" />
          </Field>
        </>
      )}
      {form.provider === 'gcp' && (
        <>
          <Field label={t('cloud.serviceAccountJson')}>
            <Textarea mono value={form.serviceAccountJson} onChange={(e) => set('serviceAccountJson', e.target.value)} required={!editing} rows={6} placeholder={editing ? keep : '{ "type": "service_account", … }'} />
          </Field>
          <Field label={t('cloud.projectId')}>
            <Input mono value={form.projectId} onChange={(e) => set('projectId', e.target.value)} placeholder={t('cloud.projectIdHint')} />
          </Field>
        </>
      )}
    </>
  )
}

/** Summary of the non-sensitive configuration for the table. */
function configSummary(c: cloudApi.CloudCredential) {
  switch (c.provider) {
    case 'aws':
      return (c.config.regions ?? []).join(', ')
    case 'azure':
      return c.config.subscriptionId ?? ''
    case 'gcp':
      return c.config.projectId ?? ''
  }
}

/**
 * Cloud provider credentials (global admin). Creating one validates against the
 * provider before saving and shows the resolved identity, which is what has
 * to be granted in each cluster's RBAC.
 */
export function CloudCredentials() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['cloud-credentials'] })
  const credsQuery = useQuery({ queryKey: ['cloud-credentials'], queryFn: cloudApi.listCloudCredentials })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [form, setForm] = useState<CredentialForm>(EMPTY_FORM)
  const [error, setError] = useState<string | null>(null)
  const [editTarget, setEditTarget] = useState<cloudApi.CloudCredential | null>(null)

  const createMutation = useMutation({
    mutationFn: cloudApi.createCloudCredential,
    onSuccess: () => {
      invalidate()
      setForm(EMPTY_FORM)
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('cloud.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: cloudApi.deleteCloudCredential,
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('cloud.error'))),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate(toPayload(form, false))
  }

  const search = filters.q.trim().toLowerCase()
  const credentials = (credsQuery.data ?? []).filter(
    (c) => !search || c.name.toLowerCase().includes(search) || c.provider.includes(search) || (c.config.identity ?? '').toLowerCase().includes(search),
  )

  const columns: DataTableColumn<cloudApi.CloudCredential>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      render: (c) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="font-medium">{c.name}</span>
          <Badge tone="neutral">{t(`cloud.provider.${c.provider}`)}</Badge>
        </span>
      ),
      sortValue: (c) => c.name,
      primary: true,
    },
    {
      key: 'identity',
      header: t('cloud.identity'),
      cellClassName: 'max-w-0 w-full font-mono text-xs text-fg-muted',
      render: (c) => (
        <div className="min-w-0">
          <div className="truncate" title={c.config.identity}>{c.config.identity}</div>
          <div className="truncate">{configSummary(c)}</div>
        </div>
      ),
      sortValue: (c) => c.config.identity ?? '',
      primary: true,
    },
    {
      key: 'clusters',
      header: t('clusterAdmin.title'),
      render: (c) => c.clusterCount,
      sortValue: (c) => c.clusterCount,
    },
  ]

  return (
    <div>
      <PageHeader
        title={t('cloud.title')}
        description={t('cloud.description')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('cloud.title') }]}
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
            data={credentials}
            rowKey={(c) => String(c.id)}
            isLoading={credsQuery.isPending}
            error={credsQuery.isError ? apiErrorText(credsQuery.error, t('common.error')) : undefined}
            filtered={search !== ''}
            emptyMessage={t('cloud.empty')}
            state={tableState}
            onStateChange={setTableState}
            actions={(c) => (
              <RowActions>
                <MenuItem onSelect={() => setEditTarget(c)}>
                  <Pencil className="h-4 w-4" aria-hidden="true" />
                  {t('clusterAdmin.edit')}
                </MenuItem>
                <MenuItem
                  danger
                  disabled={c.clusterCount > 0}
                  title={c.clusterCount > 0 ? t('cloud.inUse') : undefined}
                  onSelect={async () => {
                    if (!(await confirm({ title: t('cloud.confirmDelete', { name: c.name }), danger: true, confirmLabel: t('clusterAdmin.delete') }))) return
                    deleteMutation.mutate(c.id)
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
          <h2 className="text-sm font-semibold">{t('cloud.add')}</h2>
          <form onSubmit={handleSubmit} className="mt-3 space-y-3">
            <CredentialFields form={form} setForm={setForm} editing={false} />
            <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
              {createMutation.isPending ? t('cloud.validating') : t('cloud.add')}
            </Button>
          </form>
        </Card>
      </div>

      {editTarget && (
        <EditCredentialModal
          credential={editTarget}
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

function EditCredentialModal({ credential, onClose, onSaved }: { credential: cloudApi.CloudCredential; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation()
  const formId = useId()
  const [form, setForm] = useState<CredentialForm>(fromCredential(credential))
  const [error, setError] = useState<string | null>(null)
  const mutation = useMutation({
    mutationFn: (payload: cloudApi.CloudCredentialPayload) => cloudApi.updateCloudCredential(credential.id, payload),
    onSuccess: onSaved,
    onError: (err) => setError(apiErrorText(err, t('cloud.error'))),
  })
  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    mutation.mutate(toPayload(form, true))
  }
  return (
    <Modal
      onClose={onClose}
      size="md"
      title={t('clusterAdmin.editTitle', { name: credential.name })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={mutation.isPending}>
            {mutation.isPending ? t('cloud.validating') : t('clusterAdmin.save')}
          </Button>
        </>
      }
    >
      <form id={formId} onSubmit={handleSubmit} className="space-y-3">
        <CredentialFields form={form} setForm={setForm} editing />
        <p className="text-xs text-fg-muted">{t('cloud.rotateHint', { count: credential.clusterCount })}</p>
        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}
