import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Trash2 } from 'lucide-react'
import * as backupsApi from '../api/backups'
import { Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, Modal, PageHeader, RowActions, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { ServerBackupCard } from '../components/ServerBackupCard'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'

interface TargetForm {
  name: string
  endpoint: string
  region: string
  bucket: string
  prefix: string
  accessKeyId: string
  secretAccessKey: string
  pathStyle: boolean
  insecureSkipTLS: boolean
}
const EMPTY: TargetForm = { name: '', endpoint: '', region: 'us-east-1', bucket: '', prefix: '', accessKeyId: '', secretAccessKey: '', pathStyle: false, insecureSkipTLS: false }

function toPayload(f: TargetForm, editing: boolean): backupsApi.BackupTargetPayload {
  const out: backupsApi.BackupTargetPayload = { endpoint: f.endpoint.trim(), region: f.region.trim(), bucket: f.bucket.trim(), prefix: f.prefix.trim(), accessKeyId: f.accessKeyId.trim(), pathStyle: f.pathStyle, insecureSkipTLS: f.insecureSkipTLS }
  if (!editing) out.name = f.name.trim()
  if (f.secretAccessKey) out.secretAccessKey = f.secretAccessKey
  return out
}

function TargetFields({ form, setForm, editing }: { form: TargetForm; setForm: (fn: (f: TargetForm) => TargetForm) => void; editing: boolean }) {
  const { t } = useTranslation()
  const set = <K extends keyof TargetForm>(k: K, v: TargetForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  return (
    <>
      <Field label={t('cluster.columns.name')}>
        <Input mono value={form.name} onChange={(e) => set('name', e.target.value)} required disabled={editing} pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?" placeholder="s3-prod" />
      </Field>
      <Field label={t('backups.endpoint')}>
        <Input mono value={form.endpoint} onChange={(e) => set('endpoint', e.target.value)} placeholder={t('backups.endpointPlaceholder')} />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('backups.bucket')}>
          <Input mono value={form.bucket} onChange={(e) => set('bucket', e.target.value)} required />
        </Field>
        <Field label={t('backups.region')}>
          <Input mono value={form.region} onChange={(e) => set('region', e.target.value)} />
        </Field>
      </div>
      <Field label={t('backups.prefix')}>
        <Input mono value={form.prefix} onChange={(e) => set('prefix', e.target.value)} placeholder="bayplan" />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('cloud.fields.accessKeyId')}>
          <Input mono value={form.accessKeyId} onChange={(e) => set('accessKeyId', e.target.value)} required autoComplete="off" />
        </Field>
        <Field label={t('cloud.fields.secretAccessKey')}>
          <Input mono type="password" value={form.secretAccessKey} onChange={(e) => set('secretAccessKey', e.target.value)} required={!editing} autoComplete="new-password" placeholder={editing ? t('clusterAdmin.keepCredential') : ''} />
        </Field>
      </div>
      <Checkbox label={t('backups.pathStyle')} checked={form.pathStyle} onChange={(e) => set('pathStyle', e.target.checked)} />
      <Checkbox label={t('backups.insecure')} checked={form.insecureSkipTLS} onChange={(e) => set('insecureSkipTLS', e.target.checked)} />
    </>
  )
}

/** S3 backup targets (global admin). Creating one checks access and write permission on the bucket. */
export function BackupTargets() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['backup-targets'] })
  const query = useQuery({ queryKey: ['backup-targets'], queryFn: backupsApi.listTargets })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [form, setForm] = useState<TargetForm>(EMPTY)
  const [error, setError] = useState<string | null>(null)
  const [editTarget, setEditTarget] = useState<backupsApi.BackupTarget | null>(null)

  const createMutation = useMutation({
    mutationFn: backupsApi.createTarget,
    onSuccess: () => {
      invalidate()
      setForm(EMPTY)
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('backups.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: backupsApi.deleteTarget,
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('backups.error'))),
  })

  const search = filters.q.trim().toLowerCase()
  const targets = (query.data ?? []).filter(
    (x) => !search || x.name.toLowerCase().includes(search) || x.bucket.toLowerCase().includes(search) || (x.endpoint ?? '').toLowerCase().includes(search),
  )

  const columns: DataTableColumn<backupsApi.BackupTarget>[] = [
    { key: 'name', header: t('cluster.columns.name'), cellClassName: 'font-medium', render: (x) => x.name, sortValue: (x) => x.name, primary: true },
    {
      key: 'bucket',
      header: t('backups.bucket'),
      cellClassName: 'max-w-0 w-full font-mono text-xs',
      render: (x) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="min-w-0 max-w-full truncate">
            {x.endpoint ? x.endpoint.replace(/^https?:\/\//, '') + '/' : 's3://'}
            {x.bucket}
            {x.prefix ? `/${x.prefix}` : ''}
          </span>
          {x.insecureSkipTLS && <Badge tone="warning">{t('clusterAdmin.insecureBadge')}</Badge>}
        </span>
      ),
      sortValue: (x) => x.bucket,
      primary: true,
    },
    { key: 'schedules', header: t('backups.schedules'), render: (x) => x.scheduleCount, sortValue: (x) => x.scheduleCount },
  ]

  return (
    <div>
      <PageHeader
        title={t('backups.targetsTitle')}
        description={t('backups.targetsHint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('backups.targetsTitle') }]}
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
            data={targets}
            rowKey={(x) => String(x.id)}
            isLoading={query.isPending}
            error={query.isError ? apiErrorText(query.error, t('common.error')) : undefined}
            filtered={search !== ''}
            emptyMessage={t('backups.noTargets')}
            state={tableState}
            onStateChange={setTableState}
            actions={(x) => (
              <RowActions>
                <MenuItem onSelect={() => setEditTarget(x)}>
                  <Pencil className="h-4 w-4" aria-hidden="true" />
                  {t('clusterAdmin.edit')}
                </MenuItem>
                <MenuItem
                  danger
                  disabled={x.scheduleCount > 0}
                  title={x.scheduleCount > 0 ? t('backups.targetInUse') : undefined}
                  onSelect={async () => {
                    if (!(await confirm({ title: t('backups.confirmDeleteTarget', { name: x.name }), danger: true, confirmLabel: t('clusterAdmin.delete') }))) return
                    deleteMutation.mutate(x.id)
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
          <h2 className="text-sm font-semibold">{t('backups.addTarget')}</h2>
          <form
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              setError(null)
              createMutation.mutate(toPayload(form, false))
            }}
            className="mt-3 space-y-3"
          >
            <TargetFields form={form} setForm={setForm} editing={false} />
            <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
              {createMutation.isPending ? t('backups.validating') : t('backups.addTarget')}
            </Button>
          </form>
        </Card>
      </div>
      <ServerBackupCard targets={query.data ?? []} />
      {editTarget && (
        <EditTargetModal
          target={editTarget}
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

function EditTargetModal({ target, onClose, onSaved }: { target: backupsApi.BackupTarget; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation()
  const formId = useId()
  const [form, setForm] = useState<TargetForm>({ ...EMPTY, name: target.name, endpoint: target.endpoint ?? '', region: target.region, bucket: target.bucket, prefix: target.prefix ?? '', accessKeyId: target.accessKeyId, pathStyle: target.pathStyle, insecureSkipTLS: target.insecureSkipTLS })
  const [error, setError] = useState<string | null>(null)
  const mutation = useMutation({
    mutationFn: (p: backupsApi.BackupTargetPayload) => backupsApi.updateTarget(target.id, p),
    onSuccess: onSaved,
    onError: (err) => setError(apiErrorText(err, t('backups.error'))),
  })
  return (
    <Modal
      onClose={onClose}
      size="md"
      title={t('clusterAdmin.editTitle', { name: target.name })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={mutation.isPending}>
            {mutation.isPending ? t('backups.validating') : t('clusterAdmin.save')}
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
        <TargetFields form={form} setForm={setForm} editing />
        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}
