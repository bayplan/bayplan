import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Power, Send, Trash2 } from 'lucide-react'
import * as notifyApi from '../api/notifications'
import * as clustersApi from '../api/clusters'
import { Alert, Badge, Button, Card, Checkbox, DataTable, Field, Input, MenuItem, Modal, PageHeader, RowActions, SegmentedControl, Select, Tabs, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'
import { formatDateTime } from '../lib/dates'

const CHANNEL_TYPES: notifyApi.ChannelType[] = ['slack', 'teams', 'webhook', 'email']
type Tab = 'channels' | 'rules' | 'deliveries'


function splitList(s: string) {
  return s.split(/[,\s]+/).map((x) => x.trim()).filter(Boolean)
}

/**
 * Notifications (global admin): channels (where to), rules (what and when) and the
 * delivery history, which is the only thing that explains why an alert did not arrive.
 */
export function Notifications() {
  const { t } = useTranslation()
  const [params, setParams] = useUrlState(['tab'] as const)
  const tab: Tab = params.tab === 'rules' || params.tab === 'deliveries' ? params.tab : 'channels'
  const setTab = (v: Tab) => setParams({ tab: v === 'channels' ? '' : v })
  return (
    <div>
      <PageHeader
        title={t('notify.title')}
        description={t('notify.description')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('notify.title') }]}
      />
      <Tabs
        items={[
          { key: 'channels', label: t('notify.tabs.channels') },
          { key: 'rules', label: t('notify.tabs.rules') },
          { key: 'deliveries', label: t('notify.tabs.deliveries') },
        ]}
        active={tab}
        onChange={setTab}
      />
      <div className="mt-6">
        {tab === 'channels' && <ChannelsTab />}
        {tab === 'rules' && <RulesTab />}
        {tab === 'deliveries' && <DeliveriesTab />}
      </div>
    </div>
  )
}

// --------------------------------------------------------------- channels

interface ChannelForm {
  name: string
  type: notifyApi.ChannelType
  url: string
  headerName: string
  headerValue: string
  to: string
  insecureSkipVerify: boolean
}
const EMPTY_CHANNEL: ChannelForm = { name: '', type: 'slack', url: '', headerName: '', headerValue: '', to: '', insecureSkipVerify: false }

function channelPayload(f: ChannelForm, editing: boolean): notifyApi.ChannelPayload {
  const out: notifyApi.ChannelPayload = { name: f.name.trim(), config: {} }
  if (!editing) out.type = f.type
  if (f.type === 'email') {
    out.config = { to: splitList(f.to) }
  } else {
    out.config = { insecureSkipVerify: f.insecureSkipVerify || undefined }
    if (f.type === 'webhook' && f.headerName.trim()) out.config.headerName = f.headerName.trim()
    const secret: notifyApi.ChannelSecret = {}
    if (f.url.trim()) secret.url = f.url.trim()
    if (f.headerValue) secret.headerValue = f.headerValue
    if (Object.keys(secret).length > 0) out.secret = secret
  }
  return out
}

function ChannelFields({ form, setForm, editing }: { form: ChannelForm; setForm: (fn: (f: ChannelForm) => ChannelForm) => void; editing: boolean }) {
  const { t } = useTranslation()
  const set = <K extends keyof ChannelForm>(k: K, v: ChannelForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const keep = editing ? t('clusterAdmin.keepCredential') : ''
  return (
    <>
      {!editing && (
        <SegmentedControl
          size="sm"
          options={CHANNEL_TYPES.map((ty) => ({ value: ty, label: t(`notify.channelType.${ty}`) }))}
          value={form.type}
          onChange={(ty) => set('type', ty)}
        />
      )}
      <p className="text-xs text-fg-muted">{t(`notify.channelHint.${form.type}`)}</p>
      <Field label={t('cluster.columns.name')}>
        <Input value={form.name} onChange={(e) => set('name', e.target.value)} required />
      </Field>
      {form.type === 'email' ? (
        <Field label={t('notify.recipients')}>
          <Input mono value={form.to} onChange={(e) => set('to', e.target.value)} required placeholder="ops@example.com, sre@example.com" />
        </Field>
      ) : (
        <>
          <Field label={t('notify.webhookUrl')}>
            <Input mono type="url" value={form.url} onChange={(e) => set('url', e.target.value)} required={!editing} autoComplete="off" placeholder={editing ? keep : 'https://…'} />
          </Field>
          {form.type === 'webhook' && (
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('notify.headerName')}>
                <Input mono value={form.headerName} onChange={(e) => set('headerName', e.target.value)} placeholder="Authorization" />
              </Field>
              <Field label={t('notify.headerValue')}>
                <Input mono type="password" value={form.headerValue} onChange={(e) => set('headerValue', e.target.value)} autoComplete="new-password" placeholder={editing ? keep : 'Bearer …'} />
              </Field>
            </div>
          )}
          <Checkbox label={t('notify.insecure')} checked={form.insecureSkipVerify} onChange={(e) => set('insecureSkipVerify', e.target.checked)} />
        </>
      )}
    </>
  )
}

function channelSummary(c: notifyApi.NotificationChannel, t: (k: string) => string) {
  if (c.type === 'email') return (c.config.to ?? []).join(', ')
  return c.secretSet ? t('notify.urlSet') : t('notify.urlMissing')
}

function ChannelsTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['notify-channels'] })
  const channelsQuery = useQuery({ queryKey: ['notify-channels'], queryFn: notifyApi.listChannels })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [form, setForm] = useState<ChannelForm>(EMPTY_CHANNEL)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [editTarget, setEditTarget] = useState<notifyApi.NotificationChannel | null>(null)

  const createMutation = useMutation({
    mutationFn: notifyApi.createChannel,
    onSuccess: () => {
      invalidate()
      setForm(EMPTY_CHANNEL)
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })
  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => notifyApi.updateChannel(id, { enabled }),
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: notifyApi.deleteChannel,
    onSuccess: () => {
      invalidate()
      void queryClient.invalidateQueries({ queryKey: ['notify-rules'] })
    },
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })
  const testMutation = useMutation({
    mutationFn: notifyApi.testChannel,
    onSuccess: () => {
      setError(null)
      setNotice(t('notify.testOk'))
      void queryClient.invalidateQueries({ queryKey: ['notify-deliveries'] })
    },
    onError: (err) => {
      setNotice(null)
      setError(apiErrorText(err, t('notify.error')))
      void queryClient.invalidateQueries({ queryKey: ['notify-deliveries'] })
    },
  })

  const search = filters.q.trim().toLowerCase()
  const channels = (channelsQuery.data ?? []).filter(
    (c) => !search || c.name.toLowerCase().includes(search) || c.type.includes(search) || channelSummary(c, t).toLowerCase().includes(search),
  )

  const columns: DataTableColumn<notifyApi.NotificationChannel>[] = [
    {
      key: 'name',
      header: t('cluster.columns.name'),
      render: (c) => (
        <span className="flex flex-wrap items-center gap-1">
          <span className="font-medium">{c.name}</span>
          <Badge tone="neutral">{t(`notify.channelType.${c.type}`)}</Badge>
        </span>
      ),
      sortValue: (c) => c.name,
      primary: true,
    },
    {
      key: 'target',
      header: t('notify.target'),
      cellClassName: 'max-w-0 w-full break-words text-xs text-fg-muted font-mono',
      render: (c) => channelSummary(c, t),
    },
    {
      key: 'enabled',
      header: t('clusterAdmin.enabled'),
      sortValue: (c) => (c.enabled ? 1 : 0),
      render: (c) => <Badge tone={c.enabled ? 'success' : 'neutral'}>{c.enabled ? t('common.enabled') : t('common.disabled')}</Badge>,
      primary: true,
    },
  ]

  return (
    <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
      <div className="min-w-0 space-y-3">
        {error && <Alert tone="danger">{error}</Alert>}
        {notice && (
          <Alert tone="success" onClose={() => setNotice(null)}>
            {notice}
          </Alert>
        )}
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
          data={channels}
          rowKey={(c) => String(c.id)}
          isLoading={channelsQuery.isPending}
          error={channelsQuery.isError ? apiErrorText(channelsQuery.error, t('common.error')) : undefined}
          filtered={search !== ''}
          emptyMessage={t('notify.noChannels')}
          state={tableState}
          onStateChange={setTableState}
          actions={(c) => (
            <RowActions>
              <MenuItem onSelect={() => testMutation.mutate(c.id)} disabled={testMutation.isPending}>
                <Send className="h-4 w-4" aria-hidden="true" />
                {t('notify.test')}
              </MenuItem>
              <MenuItem onSelect={() => setEditTarget(c)}>
                <Pencil className="h-4 w-4" aria-hidden="true" />
                {t('clusterAdmin.edit')}
              </MenuItem>
              <MenuItem onSelect={() => toggleMutation.mutate({ id: c.id, enabled: !c.enabled })}>
                <Power className="h-4 w-4" aria-hidden="true" />
                {c.enabled ? t('userAdmin.disable') : t('userAdmin.enable')}
              </MenuItem>
              <MenuItem
                danger
                onSelect={async () => {
                  if (!(await confirm({ title: t('clusterAdmin.delete'), description: t('notify.confirmDeleteChannel', { name: c.name }), danger: true }))) return
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
        <h2 className="text-sm font-semibold">{t('notify.addChannel')}</h2>
        <form
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            setError(null)
            createMutation.mutate(channelPayload(form, false))
          }}
          className="mt-3 space-y-3"
        >
          <ChannelFields form={form} setForm={setForm} editing={false} />
          <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
            {t('notify.addChannel')}
          </Button>
        </form>
      </Card>
      {editTarget && (
        <EditChannelModal
          channel={editTarget}
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

function EditChannelModal({ channel, onClose, onSaved }: { channel: notifyApi.NotificationChannel; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation()
  const formId = useId()
  const [form, setForm] = useState<ChannelForm>({
    ...EMPTY_CHANNEL,
    name: channel.name,
    type: channel.type,
    headerName: channel.config.headerName ?? '',
    to: (channel.config.to ?? []).join(', '),
    insecureSkipVerify: channel.config.insecureSkipVerify ?? false,
  })
  const [error, setError] = useState<string | null>(null)
  const mutation = useMutation({
    mutationFn: (payload: notifyApi.ChannelPayload) => notifyApi.updateChannel(channel.id, payload),
    onSuccess: onSaved,
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })
  return (
    <Modal
      onClose={onClose}
      size="md"
      title={t('clusterAdmin.editTitle', { name: channel.name })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={mutation.isPending}>
            {t('clusterAdmin.save')}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(e: FormEvent) => {
          e.preventDefault()
          setError(null)
          mutation.mutate(channelPayload(form, true))
        }}
        className="space-y-3"
      >
        <ChannelFields form={form} setForm={setForm} editing />
        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}

// ------------------------------------------------------------------ rules

interface RuleForm {
  name: string
  clusterId: string // '' = all
  severities: string[]
  kinds: string[]
  reasons: string[]
  namespaces: string
  minDurationMinutes: number
  cooldownMinutes: number
  notifyResolved: boolean
  channelIds: number[]
}
const EMPTY_RULE: RuleForm = { name: '', clusterId: '', severities: ['error'], kinds: [], reasons: [], namespaces: '', minDurationMinutes: 5, cooldownMinutes: 60, notifyResolved: true, channelIds: [] }

function ruleFromApi(r: notifyApi.NotificationRule): RuleForm {
  return {
    name: r.name,
    clusterId: r.clusterId === null ? '' : String(r.clusterId),
    severities: r.severities,
    kinds: r.kinds,
    reasons: r.reasons,
    namespaces: r.namespaces.join(', '),
    minDurationMinutes: Math.round(r.minDurationSeconds / 60),
    cooldownMinutes: Math.round(r.cooldownSeconds / 60),
    notifyResolved: r.notifyResolved,
    channelIds: r.channelIds,
  }
}

function rulePayload(f: RuleForm): notifyApi.RulePayload {
  return {
    name: f.name.trim(),
    clusterId: f.clusterId ? Number(f.clusterId) : undefined,
    clearCluster: f.clusterId === '',
    severities: f.severities,
    kinds: f.kinds,
    reasons: f.reasons,
    namespaces: splitList(f.namespaces),
    minDurationSeconds: Math.max(0, f.minDurationMinutes) * 60,
    cooldownSeconds: Math.max(1, f.cooldownMinutes) * 60,
    notifyResolved: f.notifyResolved,
    channelIds: f.channelIds,
  }
}

function toggle<T>(list: T[], v: T) {
  return list.includes(v) ? list.filter((x) => x !== v) : [...list, v]
}

function RuleFields({
  form,
  setForm,
  channels,
  clusters,
  options,
}: {
  form: RuleForm
  setForm: (fn: (f: RuleForm) => RuleForm) => void
  channels: notifyApi.NotificationChannel[]
  clusters: clustersApi.Cluster[]
  options?: notifyApi.NotificationOptions
}) {
  const { t } = useTranslation()
  const set = <K extends keyof RuleForm>(k: K, v: RuleForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const chip = (active: boolean) =>
    `rounded-full border px-2 py-0.5 text-xs transition-colors ${active ? 'border-primary bg-primary text-primary-contrast' : 'border-border text-fg-muted hover:text-fg'}`
  return (
    <>
      <Field label={t('cluster.columns.name')}>
        <Input value={form.name} onChange={(e) => set('name', e.target.value)} required />
      </Field>
      <Field label={t('notify.rule.cluster')}>
        <Select value={form.clusterId} onChange={(e) => set('clusterId', e.target.value)}>
          <option value="">{t('notify.rule.allClusters')}</option>
          {clusters.filter((c) => c.kind === 'kubernetes').map((c) => (
            <option key={c.id} value={c.id}>
              {c.displayName}
            </option>
          ))}
        </Select>
      </Field>
      <div className="text-sm">
        {t('notify.rule.severities')}
        <div className="mt-1 flex flex-wrap gap-1">
          {['error', 'warning'].map((s) => (
            <button key={s} type="button" className={chip(form.severities.includes(s))} onClick={() => set('severities', toggle(form.severities, s))}>
              {t(`notify.severity.${s}`)}
            </button>
          ))}
        </div>
      </div>
      <div className="text-sm">
        {t('notify.rule.kinds')}
        <div className="mt-1 flex flex-wrap gap-1">
          {(options?.kinds ?? []).map((k) => (
            <button key={k} type="button" className={chip(form.kinds.includes(k))} onClick={() => set('kinds', toggle(form.kinds, k))}>
              {k === 'Cluster' ? t('notify.kindCluster') : k}
            </button>
          ))}
        </div>
        <span className="mt-1 block text-xs text-fg-muted">{t('notify.rule.emptyIsAll')}</span>
      </div>
      <div className="text-sm">
        {t('notify.rule.reasons')}
        <div className="mt-1 flex flex-wrap gap-1">
          {(options?.reasons ?? []).map((r) => (
            <button key={r} type="button" className={chip(form.reasons.includes(r))} onClick={() => set('reasons', toggle(form.reasons, r))} title={r}>
              {r === 'Disconnected' ? t('notify.reasonDisconnected') : t(`overview.reason.${r}`, { defaultValue: r })}
            </button>
          ))}
        </div>
      </div>
      <Field label={t('notify.rule.namespaces')}>
        <Input mono value={form.namespaces} onChange={(e) => set('namespaces', e.target.value)} placeholder={t('notify.rule.namespacesPlaceholder')} />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('notify.rule.minDuration')}>
          <Input type="number" min={0} value={form.minDurationMinutes} onChange={(e) => set('minDurationMinutes', Number(e.target.value))} />
        </Field>
        <Field label={t('notify.rule.cooldown')}>
          <Input type="number" min={1} value={form.cooldownMinutes} onChange={(e) => set('cooldownMinutes', Number(e.target.value))} />
        </Field>
      </div>
      <Checkbox label={t('notify.rule.notifyResolved')} checked={form.notifyResolved} onChange={(e) => set('notifyResolved', e.target.checked)} />
      <div className="text-sm">
        {t('notify.tabs.channels')}
        {channels.length === 0 ? (
          <Alert tone="warning" className="mt-1">
            {t('notify.noChannels')}
          </Alert>
        ) : (
          <div className="mt-1 flex flex-wrap gap-1">
            {channels.map((c) => (
              <button key={c.id} type="button" className={chip(form.channelIds.includes(c.id))} onClick={() => set('channelIds', toggle(form.channelIds, c.id))}>
                {c.name}
              </button>
            ))}
          </div>
        )}
      </div>
    </>
  )
}

function RulesTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['notify-rules'] })
  const rulesQuery = useQuery({ queryKey: ['notify-rules'], queryFn: notifyApi.listRules })
  const channelsQuery = useQuery({ queryKey: ['notify-channels'], queryFn: notifyApi.listChannels })
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: clustersApi.listClusters })
  const optionsQuery = useQuery({ queryKey: ['notify-options'], queryFn: notifyApi.getNotificationOptions, staleTime: Infinity })
  const [tableState, setTableState] = useTableState({ sortKey: 'name' })
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [form, setForm] = useState<RuleForm>(EMPTY_RULE)
  const [error, setError] = useState<string | null>(null)
  const [editTarget, setEditTarget] = useState<notifyApi.NotificationRule | null>(null)
  const channels = channelsQuery.data ?? []
  const clusters = clustersQuery.data ?? []
  const channelName = (id: number) => channels.find((c) => c.id === id)?.name ?? `#${id}`
  const clusterName = (id: number | null) => (id === null ? t('notify.rule.allClusters') : (clusters.find((c) => c.id === id)?.displayName ?? `#${id}`))

  const createMutation = useMutation({
    mutationFn: notifyApi.createRule,
    onSuccess: () => {
      invalidate()
      setForm(EMPTY_RULE)
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })
  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => notifyApi.updateRule(id, { enabled }),
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })
  const deleteMutation = useMutation({
    mutationFn: notifyApi.deleteRule,
    onSuccess: invalidate,
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })

  const search = filters.q.trim().toLowerCase()
  const rules = (rulesQuery.data ?? []).filter((r) => !search || r.name.toLowerCase().includes(search) || clusterName(r.clusterId).toLowerCase().includes(search))

  const columns: DataTableColumn<notifyApi.NotificationRule>[] = [
    { key: 'name', header: t('cluster.columns.name'), cellClassName: 'font-medium', render: (r) => r.name, sortValue: (r) => r.name, primary: true },
    {
      key: 'scope',
      header: t('notify.rule.scope'),
      cellClassName: 'max-w-0 w-full text-xs',
      render: (r) => (
        <div className="space-y-0.5">
          <div>{clusterName(r.clusterId)}</div>
          <div className="flex flex-wrap gap-1">
            {r.severities.map((s) => (
              <Badge key={s} tone={s === 'error' ? 'danger' : 'warning'}>
                {t(`notify.severity.${s}`)}
              </Badge>
            ))}
            {r.kinds.map((k) => (
              <Badge key={k} tone="neutral">
                {k}
              </Badge>
            ))}
            {r.reasons.map((x) => (
              <Badge key={x} tone="neutral">
                {x}
              </Badge>
            ))}
            {r.namespaces.length > 0 && <Badge tone="neutral">ns: {r.namespaces.join(', ')}</Badge>}
          </div>
        </div>
      ),
    },
    {
      key: 'timing',
      header: t('notify.rule.timing'),
      cellClassName: 'text-xs text-fg-muted',
      render: (r) => t('notify.rule.timingValue', { min: Math.round(r.minDurationSeconds / 60), cooldown: Math.round(r.cooldownSeconds / 60) }),
    },
    {
      key: 'channels',
      header: t('notify.tabs.channels'),
      cellClassName: 'text-xs',
      render: (r) => (
        <span className="flex flex-wrap gap-1">
          {r.channelIds.map((id) => (
            <Badge key={id} tone="neutral">
              {channelName(id)}
            </Badge>
          ))}
        </span>
      ),
    },
    {
      key: 'enabled',
      header: t('clusterAdmin.enabled'),
      sortValue: (r) => (r.enabled ? 1 : 0),
      render: (r) => <Badge tone={r.enabled ? 'success' : 'neutral'}>{r.enabled ? t('common.enabled') : t('common.disabled')}</Badge>,
      primary: true,
    },
  ]

  return (
    <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
      <div className="min-w-0 space-y-3">
        {error && <Alert tone="danger">{error}</Alert>}
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
          data={rules}
          rowKey={(r) => String(r.id)}
          isLoading={rulesQuery.isPending}
          error={rulesQuery.isError ? apiErrorText(rulesQuery.error, t('common.error')) : undefined}
          filtered={search !== ''}
          emptyMessage={t('notify.noRules')}
          state={tableState}
          onStateChange={setTableState}
          actions={(r) => (
            <RowActions>
              <MenuItem onSelect={() => setEditTarget(r)}>
                <Pencil className="h-4 w-4" aria-hidden="true" />
                {t('clusterAdmin.edit')}
              </MenuItem>
              <MenuItem onSelect={() => toggleMutation.mutate({ id: r.id, enabled: !r.enabled })}>
                <Power className="h-4 w-4" aria-hidden="true" />
                {r.enabled ? t('userAdmin.disable') : t('userAdmin.enable')}
              </MenuItem>
              <MenuItem
                danger
                onSelect={async () => {
                  if (!(await confirm({ title: t('clusterAdmin.delete'), description: t('notify.confirmDeleteRule', { name: r.name }), danger: true }))) return
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
        <h2 className="text-sm font-semibold">{t('notify.addRule')}</h2>
        <p className="mt-1 text-xs text-fg-muted">{t('notify.ruleHint')}</p>
        <form
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            setError(null)
            createMutation.mutate(rulePayload(form))
          }}
          className="mt-3 space-y-3"
        >
          <RuleFields form={form} setForm={setForm} channels={channels} clusters={clusters} options={optionsQuery.data} />
          <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
            {t('notify.addRule')}
          </Button>
        </form>
      </Card>
      {editTarget && (
        <EditRuleModal
          rule={editTarget}
          channels={channels}
          clusters={clusters}
          options={optionsQuery.data}
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

function EditRuleModal({
  rule,
  channels,
  clusters,
  options,
  onClose,
  onSaved,
}: {
  rule: notifyApi.NotificationRule
  channels: notifyApi.NotificationChannel[]
  clusters: clustersApi.Cluster[]
  options?: notifyApi.NotificationOptions
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const formId = useId()
  const [form, setForm] = useState<RuleForm>(ruleFromApi(rule))
  const [error, setError] = useState<string | null>(null)
  const mutation = useMutation({
    mutationFn: (payload: notifyApi.RulePayload) => notifyApi.updateRule(rule.id, payload),
    onSuccess: onSaved,
    onError: (err) => setError(apiErrorText(err, t('notify.error'))),
  })
  return (
    <Modal
      onClose={onClose}
      size="md"
      title={t('clusterAdmin.editTitle', { name: rule.name })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={mutation.isPending}>
            {t('clusterAdmin.save')}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(e: FormEvent) => {
          e.preventDefault()
          setError(null)
          mutation.mutate(rulePayload(form))
        }}
        className="space-y-3"
      >
        <RuleFields form={form} setForm={setForm} channels={channels} clusters={clusters} options={options} />
        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}

// ------------------------------------------------------------- deliveries

function DeliveriesTab() {
  const { t, i18n } = useTranslation()
  const deliveriesQuery = useQuery({ queryKey: ['notify-deliveries'], queryFn: () => notifyApi.listDeliveries(200), refetchInterval: 60_000 })
  const [tableState, setTableState] = useTableState({ sortKey: 'ts', sortDir: 'desc' })
  const columns: DataTableColumn<notifyApi.Delivery>[] = [
    {
      key: 'ts',
      header: t('notify.deliveries.when'),
      cellClassName: 'text-xs whitespace-nowrap text-fg-muted',
      render: (d) => formatDateTime(d.ts, i18n.resolvedLanguage),
      sortValue: (d) => d.ts,
      primary: true,
    },
    {
      key: 'event',
      header: t('notify.deliveries.event'),
      render: (d) => <Badge tone={d.event === 'firing' ? 'danger' : d.event === 'resolved' ? 'success' : 'neutral'}>{t(`notify.event.${d.event}`)}</Badge>,
      sortValue: (d) => d.event,
    },
    {
      key: 'title',
      header: t('notify.deliveries.title'),
      cellClassName: 'max-w-0 w-full',
      primary: true,
      render: (d) => (
        <div className="min-w-0">
          <div className="truncate">{d.title}</div>
          <div className="truncate text-xs text-fg-muted">
            {d.ruleName} → {d.channelName}
          </div>
        </div>
      ),
    },
    {
      key: 'ok',
      header: t('notify.deliveries.result'),
      render: (d) => (d.ok ? <Badge tone="success">OK</Badge> : <span className="text-xs break-all text-danger">{d.error}</span>),
      sortValue: (d) => (d.ok ? 1 : 0),
      primary: true,
    },
  ]
  return (
    <DataTable
      columns={columns}
      data={deliveriesQuery.data ?? []}
      rowKey={(d) => String(d.id)}
      isLoading={deliveriesQuery.isPending}
      error={deliveriesQuery.isError ? apiErrorText(deliveriesQuery.error, t('common.error')) : undefined}
      emptyMessage={t('notify.deliveries.empty')}
      state={tableState}
      onStateChange={setTableState}
    />
  )
}
