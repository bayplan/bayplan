import { useId, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, KeyRound, Trash2 } from 'lucide-react'
import * as tokensApi from '../api/tokens'
import { Age, Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, Modal, RowActions, Select, useConfirm, type DataTableColumn } from './ui'
import { apiErrorText } from '../lib/errors'
import { useCopy } from '../hooks/useCopy'
import { formatDate } from '../lib/dates'

/**
 * The user's personal API tokens: create (the value is shown ONCE), list and
 * revoke. Only available with a browser session: a token cannot manage tokens
 * (the backend rejects it).
 */
export function ApiTokensCard() {
  const { t, i18n } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const tokensQuery = useQuery({ queryKey: ['api-tokens'], queryFn: tokensApi.listTokens })
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['api-tokens'] })
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState({ name: '', readOnly: true, expiresInDays: 90 })
  const [created, setCreated] = useState<{ token: string; info: tokensApi.APIToken } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const { copy: copyText, copied } = useCopy()
  const formId = useId()

  const createMutation = useMutation({
    mutationFn: tokensApi.createToken,
    onSuccess: (res) => {
      invalidate()
      setCreated(res)
      setForm({ name: '', readOnly: true, expiresInDays: 90 })
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('tokens.error'))),
  })
  const revokeMutation = useMutation({
    mutationFn: tokensApi.revokeToken,
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('tokens.error'))),
  })

  async function copy() {
    if (created) await copyText(created.token)
  }

  const active = (tokensQuery.data ?? []).filter((x) => !x.revokedAt)
  const revoked = (tokensQuery.data ?? []).filter((x) => x.revokedAt)

  const columns: DataTableColumn<tokensApi.APIToken>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      primary: true,
      render: (tok) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="font-medium">{tok.name}</span>
          <code className="text-xs text-fg-muted">{tok.prefix}…</code>
          <Badge tone={tok.readOnly ? 'neutral' : 'warning'}>{tok.readOnly ? t('tokens.readOnly') : t('tokens.fullAccess')}</Badge>
        </span>
      ),
    },
    { key: 'created', header: t('tokens.created'), cellClassName: 'text-xs text-fg-muted whitespace-nowrap', render: (tok) => <Age createdAt={tok.createdAt} /> },
    {
      key: 'lastUsed',
      header: t('tokens.lastUsed'),
      cellClassName: 'text-xs text-fg-muted whitespace-nowrap',
      render: (tok) => (tok.lastUsedAt ? <Age createdAt={tok.lastUsedAt} /> : t('tokens.neverUsed')),
    },
    {
      key: 'expires',
      header: t('tokens.expiresIn'),
      cellClassName: 'text-xs text-fg-muted whitespace-nowrap',
      primary: true,
      render: (tok) => (tok.expiresAt ? t('tokens.expires', { when: formatDate(tok.expiresAt, i18n.resolvedLanguage) }) : t('tokens.noExpiry')),
    },
  ]

  return (
    <Card className="p-5">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <KeyRound className="h-4 w-4" aria-hidden="true" />
            {t('tokens.title')}
          </h2>
          <p className="mt-1 text-xs text-fg-muted">
            {t('tokens.hint')}{' '}
            <Link to="/api-docs" className="underline">
              {t('tokens.apiDocs')}
            </Link>
          </p>
        </div>
        <Button size="sm" variant="primary" onClick={() => setOpen(true)}>
          {t('tokens.create')}
        </Button>
      </div>
      {error && (
        <Alert tone="danger" className="mt-2">
          {error}
        </Alert>
      )}
      <div className="mt-3">
        <DataTable
          columns={columns}
          data={active}
          rowKey={(tok) => String(tok.id)}
          isLoading={tokensQuery.isPending}
          error={tokensQuery.isError ? apiErrorText(tokensQuery.error, t('common.error')) : undefined}
          emptyMessage={t('tokens.empty')}
          actions={(tok) => (
            <RowActions>
              <MenuItem
                danger
                onSelect={async () => {
                  if (!(await confirm({ title: t('tokens.confirmRevoke', { name: tok.name }), danger: true, confirmLabel: t('tokens.revoke') }))) return
                  revokeMutation.mutate(tok.id)
                }}
              >
                <Trash2 className="h-4 w-4" aria-hidden="true" />
                {t('tokens.revoke')}
              </MenuItem>
            </RowActions>
          )}
        />
      </div>
      {revoked.length > 0 && (
        <p className="mt-3 text-xs text-fg-muted">{t('tokens.revokedCount', { count: revoked.length })}</p>
      )}

      {open && !created && (
        <Modal
          onClose={() => setOpen(false)}
          variant="center"
          size="md"
          title={t('tokens.create')}
          footer={
            <>
              <Button variant="secondary" onClick={() => setOpen(false)}>
                {t('clusterAdmin.cancel')}
              </Button>
              <Button type="submit" form={formId} variant="primary" disabled={createMutation.isPending}>
                {t('tokens.create')}
              </Button>
            </>
          }
        >
          <form
            id={formId}
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              setError(null)
              createMutation.mutate(form)
            }}
            className="space-y-3"
          >
            <Field label={t('cluster.columns.name')}>
              <Input value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} required maxLength={60} placeholder="ci-deploy" autoFocus />
            </Field>
            <Field label={t('tokens.expiresIn')}>
              <Select value={form.expiresInDays} onChange={(e) => setForm((f) => ({ ...f, expiresInDays: Number(e.target.value) }))}>
                <option value={30}>30 {t('tokens.days')}</option>
                <option value={90}>90 {t('tokens.days')}</option>
                <option value={365}>365 {t('tokens.days')}</option>
                <option value={0}>{t('tokens.noExpiry')}</option>
              </Select>
            </Field>
            <Checkbox label={t('tokens.readOnly')} hint={t('tokens.readOnlyHint')} checked={form.readOnly} onChange={(e) => setForm((f) => ({ ...f, readOnly: e.target.checked }))} />
            {error && <Alert tone="danger">{error}</Alert>}
          </form>
        </Modal>
      )}

      {created && (
        <Modal
          onClose={() => {
            setCreated(null)
            setOpen(false)
          }}
          variant="center"
          size="lg"
          title={t('tokens.createdTitle', { name: created.info.name })}
          footer={
            <Button
              variant="primary"
              onClick={() => {
                setCreated(null)
                setOpen(false)
              }}
            >
              {t('common.close')}
            </Button>
          }
        >
          <Alert tone="warning">{t('tokens.createdHint')}</Alert>
          <div className="mt-3 flex items-start gap-2">
            <code className="flex-1 overflow-x-auto rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs break-all">{created.token}</code>
            <Button size="sm" onClick={() => void copy()}>
              <Copy className="h-3.5 w-3.5" aria-hidden="true" />
              {copied ? t('common.copied') : t('common.copy')}
            </Button>
          </div>
          <p className="mt-4 text-xs text-fg-muted">{t('tokens.usage')}</p>
          <pre className="mt-1 overflow-x-auto rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs">{`curl -H "Authorization: Bearer ${created.token}" ${window.location.origin}/api/clusters`}</pre>
          <p className="mt-4 text-xs text-fg-muted">{t('tokens.mcpUsage')}</p>
          <pre className="mt-1 overflow-x-auto rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs">{`claude mcp add --transport http bayplan ${window.location.origin}/api/mcp --header "Authorization: Bearer ${created.token}"`}</pre>
          <p className="mt-1 text-xs text-fg-muted">{t('tokens.mcpHint', { url: `${window.location.origin}/api/mcp` })}</p>
        </Modal>
      )}
    </Card>
  )
}
