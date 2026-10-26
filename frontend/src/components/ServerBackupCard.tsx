import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, Play } from 'lucide-react'
import * as backupsApi from '../api/backups'
import { Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, Select, type DataTableColumn } from './ui'
import { apiErrorText } from '../lib/errors'
import { formatBytes } from '../lib/format'

const DEFAULTS: backupsApi.ServerBackupConfig = { enabled: false, targetId: 0, intervalHours: 24, keep: 14 }

/**
 * Backups of Bayplan's OWN database, as opposed to the per-cluster ones: a
 * pg_dump to one of the targets on a schedule, plus a download on the spot for
 * installations without S3. Restoring is a CLI step on an empty database, and
 * the card says so instead of offering a button that could only half work.
 */
export function ServerBackupCard({ targets }: { targets: backupsApi.BackupTarget[] }) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery({ queryKey: ['server-backup'], queryFn: backupsApi.getServerBackup, refetchInterval: 60_000 })
  const [form, setForm] = useState<backupsApi.ServerBackupConfig>(DEFAULTS)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  // The form follows the server only while the user has not touched it: the
  // 60 s poll (there for the run history) used to wipe whatever was being
  // typed. Saving clears it, so the saved values come back from the server.
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    if (query.data && !dirty) setForm(query.data.config.targetId ? query.data.config : { ...DEFAULTS, ...query.data.config, intervalHours: query.data.config.intervalHours || 24, keep: query.data.config.keep || 14 })
  }, [query.data, dirty])

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['server-backup'] })
  const save = useMutation({
    mutationFn: backupsApi.updateServerBackup,
    onSuccess: () => {
      setError(null)
      setSaved(true)
      setDirty(false)
      invalidate()
    },
    onError: (err) => {
      setSaved(false)
      setError(apiErrorText(err, t('backups.error')))
    },
  })
  const run = useMutation({
    mutationFn: backupsApi.runServerBackup,
    onSuccess: () => {
      setError(null)
      invalidate()
    },
    onError: (err) => {
      setError(apiErrorText(err, t('backups.error')))
      invalidate()
    },
  })

  const set = <K extends keyof backupsApi.ServerBackupConfig>(k: K, v: backupsApi.ServerBackupConfig[K]) => {
    setSaved(false)
    setDirty(true)
    setForm((f) => ({ ...f, [k]: v }))
  }

  const columns: DataTableColumn<backupsApi.ServerBackupRun>[] = [
    {
      key: 'when',
      header: t('backups.when'),
      primary: true,
      cellClassName: 'text-xs whitespace-nowrap',
      render: (r) => (
        <div>
          <div>{new Date(r.startedAt).toLocaleString(i18n.language)}</div>
          <div className="text-fg-muted">
            {r.targetName} · {t(`backups.trigger.${r.trigger}`)}
          </div>
        </div>
      ),
    },
    {
      key: 'status',
      header: t('cluster.columns.status'),
      primary: true,
      cellClassName: 'max-w-0 w-full',
      render: (r) =>
        r.status === 'ok' ? (
          <span className="flex flex-wrap items-center gap-2 text-xs">
            <Badge tone="success">OK</Badge>
            {formatBytes(r.sizeBytes, i18n.language)}
          </span>
        ) : r.status === 'running' ? (
          <Badge tone="warning">{t('backups.running')}</Badge>
        ) : (
          <span className="text-xs break-words text-danger">{r.error}</span>
        ),
    },
  ]

  const available = query.data?.available ?? true

  return (
    <Card className="mt-8 p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">{t('backups.server.title')}</h2>
          <p className="mt-1 max-w-3xl text-xs text-fg-muted">{t('backups.server.hint')}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {/* A navigation, not an XHR: with Content-Disposition the browser
              streams the dump straight to disk and stays on the page. */}
          <Button variant="secondary" size="sm" disabled={!available} onClick={() => window.location.assign(backupsApi.serverBackupDownloadUrl)}>
            <Download aria-hidden="true" />
            {t('backups.server.download')}
          </Button>
          <Button variant="secondary" size="sm" disabled={!available || !query.data?.config.targetId || run.isPending} onClick={() => run.mutate()}>
            <Play aria-hidden="true" />
            {run.isPending ? t('backups.running') : t('backups.runNow')}
          </Button>
        </div>
      </div>

      {!available && (
        <Alert tone="warning" className="mt-4">
          {t('backups.server.unavailable')}
        </Alert>
      )}
      {error && (
        <Alert tone="danger" className="mt-4">
          {error}
        </Alert>
      )}

      <div className="mt-4 grid gap-6 lg:grid-cols-[2fr_3fr]">
        <form
          className="space-y-3"
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            setError(null)
            save.mutate(form)
          }}
        >
          <Checkbox label={t('backups.server.enabled')} checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} />
          <Field label={t('backups.target')}>
            <Select value={form.targetId || ''} onChange={(e) => set('targetId', Number(e.target.value) || 0)} required={form.enabled}>
              <option value="">{t('backups.server.chooseTarget')}</option>
              {targets.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.name}
                </option>
              ))}
            </Select>
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('backups.server.intervalHours')}>
              <Input type="number" min={1} max={720} value={form.intervalHours} onChange={(e) => set('intervalHours', Number(e.target.value))} />
            </Field>
            <Field label={t('backups.server.keep')}>
              <Input type="number" min={1} max={365} value={form.keep} onChange={(e) => set('keep', Number(e.target.value))} />
            </Field>
          </div>
          {saved && <Alert tone="success">{t('settings.saved')}</Alert>}
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t('settings.save')}
          </Button>
          <p className="text-xs text-fg-muted">{t('backups.server.restoreHint')}</p>
        </form>
        <div className="min-w-0">
          <h3 className="mb-2 text-xs font-semibold text-fg-muted uppercase">{t('backups.runs')}</h3>
          <DataTable
            columns={columns}
            data={query.data?.runs ?? []}
            rowKey={(r) => String(r.id)}
            isLoading={query.isPending}
            error={query.isError ? apiErrorText(query.error, t('common.error')) : undefined}
            emptyMessage={t('backups.noRuns')}
          />
        </div>
      </div>
    </Card>
  )
}
