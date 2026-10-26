import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as settingsApi from '../api/settings'
import { Alert, Button, Card, Checkbox, Field, Input, PageHeader, Select, Textarea } from '../components/ui'
import { OIDCSettingsCard } from '../components/settings/OIDCSettingsCard'
import { AISettingsCard } from '../components/settings/AISettingsCard'
import { apiErrorText } from '../lib/errors'
import { useUrlState } from '../hooks/useUrlState'
import { cn } from '../lib/cn'

type SectionId = 'general' | 'agents' | 'auth' | 'smtp' | 'ai' | 'costs'

/**
 * Instance settings. What used to require environment variables and a
 * redeploy (hardcoded brand, SSO via AZURE_*) is edited here and applied
 * hot. Only the irreducible bootstrap remains in the environment: DB, encryption
 * key and port.
 *
 * One section at a time with a side menu: side by side cards of very different heights read as a pile. The section is in the
 * URL (a link can point at "the SSO settings"), and each entry says whether it
 * is configured so the admin sees what is missing without opening anything.
 */
export function Settings() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({ queryKey: ['settings'], queryFn: settingsApi.getSettings })
  const [filters, setFilters] = useUrlState(['section'] as const)
  const data = settingsQuery.data
  const onSaved = () => void queryClient.invalidateQueries({ queryKey: ['settings'] })

  const sections: { id: SectionId; configured: boolean }[] = data
    ? [
        { id: 'general', configured: data.server.publicURL.trim() !== '' },
        // Works with the defaults (same-version image, system CAs).
        { id: 'agents', configured: true },
        ...(data.oidc ? [{ id: 'auth' as const, configured: data.oidc.enabled }] : []),
        { id: 'smtp', configured: data.smtp.host.trim() !== '' },
        { id: 'ai', configured: data.ai.enabled && data.ai.model !== '' },
        { id: 'costs', configured: data.costs.cpuCoreHour > 0 || data.costs.memoryGiBHour > 0 },
      ]
    : []
  // A value from the URL is user input: an unknown one falls back to the first.
  const active: SectionId = sections.some((s) => s.id === filters.section) ? (filters.section as SectionId) : 'general'

  return (
    <div>
      <PageHeader
        title={t('settings.title')}
        description={t('settings.subtitle')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('settings.title') }]}
      />

      {settingsQuery.isPending && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
      {settingsQuery.isError && <Alert tone="danger">{apiErrorText(settingsQuery.error, t('common.error'))}</Alert>}

      {data && (
        <div className="flex flex-col gap-6 md:flex-row md:items-start">
          {/* Phone: a select instead of a side column that would push the form down. */}
          <div className="md:hidden">
            <Select value={active} onChange={(e) => setFilters({ section: e.target.value })} aria-label={t('settings.sectionsLabel')}>
              {sections.map((s) => (
                <option key={s.id} value={s.id}>
                  {t(`settings.nav.${s.id}`)} · {s.configured ? t('settings.configured') : t('settings.notConfigured')}
                </option>
              ))}
            </Select>
          </div>
          <nav aria-label={t('settings.sectionsLabel')} className="hidden w-56 shrink-0 md:block">
            <ul className="space-y-0.5">
              {sections.map((s) => (
                <li key={s.id}>
                  <button
                    type="button"
                    onClick={() => setFilters({ section: s.id === 'general' ? '' : s.id })}
                    aria-current={active === s.id ? 'page' : undefined}
                    className={cn(
                      'flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-sm text-fg-muted hover:bg-surface-hover hover:text-fg',
                      active === s.id && 'bg-primary-soft font-medium text-fg',
                    )}
                  >
                    <span className="flex-1">{t(`settings.nav.${s.id}`)}</span>
                    <span
                      className={cn('h-2 w-2 shrink-0 rounded-full', s.configured ? 'bg-success' : 'border border-border-strong')}
                      title={s.configured ? t('settings.configured') : t('settings.notConfigured')}
                    />
                    <span className="sr-only">{s.configured ? t('settings.configured') : t('settings.notConfigured')}</span>
                  </button>
                </li>
              ))}
            </ul>
          </nav>

          <div className="min-w-0 max-w-3xl flex-1">
            {active === 'general' && <GeneralCard initial={data.server} onSaved={onSaved} />}
            {active === 'agents' && (
              <AgentsCard initial={data.server} serverVersion={data.serverVersion} defaultAgentImage={data.defaultAgentImage} agentCAAuto={data.agentCAAuto} onSaved={onSaved} />
            )}
            {active === 'auth' && data.oidc && (
              <OIDCSettingsCard
                initial={data.oidc}
                publicURL={data.server.publicURL}
                onSaved={() => {
                  onSaved()
                  void queryClient.invalidateQueries({ queryKey: ['auth-status'] })
                }}
              />
            )}
            {active === 'smtp' && <SMTPCard initial={data.smtp} onSaved={onSaved} />}
            {active === 'ai' && <AISettingsCard initial={data.ai} onSaved={onSaved} />}
            {active === 'costs' && <CostsCard initial={data.costs} onSaved={onSaved} />}
          </div>
        </div>
      )}
    </div>
  )
}

/** Shared save flow of the two halves of the "server" section. */
function useServerSave(onSaved: () => void) {
  const { t } = useTranslation()
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const mutation = useMutation({
    mutationFn: settingsApi.updateServer,
    onSuccess: () => {
      setError(null)
      setSaved(true)
      onSaved()
    },
    onError: (err) => {
      setSaved(false)
      setError(apiErrorText(err, t('settings.saveError')))
    },
  })
  return { mutation, error, saved, setSaved }
}

// General and Agents are two screens over ONE settings section ("server"):
// each saves the whole section with its own fields changed and the other's
// as stored, so saving one never blanks the other.
function GeneralCard({ initial, onSaved }: { initial: settingsApi.ServerSettings; onSaved: () => void }) {
  const { t } = useTranslation()
  const [publicURL, setPublicURL] = useState(initial.publicURL)
  const { mutation, error, saved, setSaved } = useServerSave(onSaved)
  useEffect(() => setPublicURL(initial.publicURL), [initial])

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSaved(false)
    mutation.mutate({ ...initial, publicURL })
  }

  return (
    <Card className="p-5">
      <h2 className="text-sm font-semibold">{t('settings.server.title')}</h2>
      <p className="mt-1 text-xs text-fg-muted">{t('settings.server.hint')}</p>
      <form onSubmit={handleSubmit} className="mt-4 space-y-3">
        <Field label={t('settings.server.publicURL')} hint={t('settings.server.publicURLHint')}>
          <Input mono type="url" value={publicURL} onChange={(e) => setPublicURL(e.target.value)} placeholder={window.location.origin} />
        </Field>
        {error && <Alert tone="danger">{error}</Alert>}
        {saved && <Alert tone="success">{t('settings.saved')}</Alert>}
        <Button type="submit" variant="primary" disabled={mutation.isPending}>
          {t('settings.save')}
        </Button>
      </form>
    </Card>
  )
}

function AgentsCard({ initial, onSaved, serverVersion, defaultAgentImage, agentCAAuto }: { initial: settingsApi.ServerSettings; onSaved: () => void; serverVersion: string; defaultAgentImage: string; agentCAAuto: boolean }) {
  const { t } = useTranslation()
  const [agentImage, setAgentImage] = useState(initial.agentImage ?? '')
  const [agentCACert, setAgentCACert] = useState(initial.agentCACert ?? '')
  const { mutation, error, saved, setSaved } = useServerSave(onSaved)
  useEffect(() => {
    setAgentImage(initial.agentImage ?? '')
    setAgentCACert(initial.agentCACert ?? '')
  }, [initial])

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSaved(false)
    mutation.mutate({ ...initial, agentImage, agentCACert })
  }

  return (
    <Card className="p-5">
      <h2 className="text-sm font-semibold">{t('settings.agents.title')}</h2>
      <p className="mt-1 text-xs text-fg-muted">{t('settings.agents.hint')}</p>
      <form onSubmit={handleSubmit} className="mt-4 space-y-3">
        <Field label={t('settings.server.agentImage')} hint={t('settings.server.agentImageHint', { version: serverVersion, image: defaultAgentImage })}>
          <Input mono value={agentImage} onChange={(e) => setAgentImage(e.target.value)} placeholder={defaultAgentImage} />
        </Field>
        <Field label={t('settings.server.agentCACert')} hint={agentCAAuto ? t('settings.server.agentCACertBuiltin') : t('settings.server.agentCACertHint')}>
          <Textarea mono rows={6} value={agentCACert} onChange={(e) => setAgentCACert(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" spellCheck={false} />
        </Field>
        {error && <Alert tone="danger">{error}</Alert>}
        {saved && <Alert tone="success">{t('settings.saved')}</Alert>}
        <Button type="submit" variant="primary" disabled={mutation.isPending}>
          {t('settings.save')}
        </Button>
      </form>
    </Card>
  )
}

/** Outgoing mail for the email notification channels. An empty password keeps the current one. */
function SMTPCard({ initial, onSaved }: { initial: settingsApi.SMTPSettings; onSaved: () => void }) {
  const { t } = useTranslation()
  const [form, setForm] = useState({ ...initial, password: '' })
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    setForm({ ...initial, password: '' })
  }, [initial])
  const set = <K extends keyof typeof form>(k: K, v: (typeof form)[K]) => setForm((f) => ({ ...f, [k]: v }))
  const mutation = useMutation({
    mutationFn: settingsApi.updateSMTP,
    onSuccess: () => {
      setError(null)
      setSaved(true)
      onSaved()
    },
    onError: (err) => {
      setSaved(false)
      setError(apiErrorText(err, t('settings.saveError')))
    },
  })
  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSaved(false)
    const { passwordSet: _unused, ...rest } = form
    void _unused
    mutation.mutate({ ...rest, port: Number(rest.port) || 0, password: rest.password || undefined })
  }
  return (
    <Card className="p-5">
      <h2 className="text-sm font-semibold">{t('settings.smtp.title')}</h2>
      <p className="mt-1 text-xs text-fg-muted">{t('settings.smtp.hint')}</p>
      <form onSubmit={handleSubmit} className="mt-4 space-y-3">
        <div className="grid gap-3 sm:grid-cols-[2fr_1fr]">
          <Field label={t('settings.smtp.host')}>
            <Input mono value={form.host} onChange={(e) => set('host', e.target.value)} placeholder="smtp.example.com" />
          </Field>
          <Field label={t('settings.smtp.port')}>
            <Input mono type="number" min={0} max={65535} value={form.port || ''} onChange={(e) => set('port', Number(e.target.value))} placeholder={form.startTLS ? '587' : '465'} />
          </Field>
        </div>
        <Field label={t('settings.smtp.from')}>
          <Input mono type="email" value={form.from} onChange={(e) => set('from', e.target.value)} placeholder="bayplan@example.com" />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('login.username')}>
            <Input value={form.username} onChange={(e) => set('username', e.target.value)} autoComplete="off" />
          </Field>
          <Field label={t('login.password')}>
            <Input type="password" value={form.password} onChange={(e) => set('password', e.target.value)} autoComplete="new-password" placeholder={initial.passwordSet ? t('settings.oidc.secretKept') : ''} />
          </Field>
        </div>
        <Checkbox label={t('settings.smtp.startTLS')} checked={form.startTLS} onChange={(e) => set('startTLS', e.target.checked)} />
        <Checkbox label={t('settings.smtp.insecure')} checked={form.insecureSkipVerify} onChange={(e) => set('insecureSkipVerify', e.target.checked)} />
        {error && <Alert tone="danger">{error}</Alert>}
        {saved && <Alert tone="success">{t('settings.saved')}</Alert>}
        <Button type="submit" variant="primary" disabled={mutation.isPending}>
          {t('settings.save')}
        </Button>
      </form>
    </Card>
  )
}

function CostsCard({ initial, onSaved }: { initial: settingsApi.CostSettings; onSaved: () => void }) {
  const { t } = useTranslation()
  const [form, setForm] = useState({ cpu: String(initial.cpuCoreHour || ''), mem: String(initial.memoryGiBHour || ''), currency: initial.currency || 'EUR' })
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    setForm({ cpu: String(initial.cpuCoreHour || ''), mem: String(initial.memoryGiBHour || ''), currency: initial.currency || 'EUR' })
  }, [initial])
  const mutation = useMutation({
    mutationFn: settingsApi.updateCosts,
    onSuccess: () => {
      setError(null)
      setSaved(true)
      onSaved()
    },
    onError: (err) => {
      setSaved(false)
      setError(apiErrorText(err, t('settings.saveError')))
    },
  })
  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSaved(false)
    mutation.mutate({ cpuCoreHour: Number(form.cpu) || 0, memoryGiBHour: Number(form.mem) || 0, currency: form.currency.trim() || 'EUR' })
  }
  return (
    <Card className="p-5">
      <h2 className="text-sm font-semibold">{t('settings.costs.title')}</h2>
      <p className="mt-1 text-xs text-fg-muted">{t('settings.costs.hint')}</p>
      <form onSubmit={handleSubmit} className="mt-4 space-y-3">
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label={t('settings.costs.cpu')}>
            <Input mono type="number" min={0} step="any" value={form.cpu} onChange={(e) => setForm((f) => ({ ...f, cpu: e.target.value }))} placeholder="0.04" />
          </Field>
          <Field label={t('settings.costs.memory')}>
            <Input mono type="number" min={0} step="any" value={form.mem} onChange={(e) => setForm((f) => ({ ...f, mem: e.target.value }))} placeholder="0.005" />
          </Field>
          <Field label={t('settings.costs.currency')}>
            <Input mono value={form.currency} maxLength={8} onChange={(e) => setForm((f) => ({ ...f, currency: e.target.value }))} placeholder="EUR" />
          </Field>
        </div>
        {error && <Alert tone="danger">{error}</Alert>}
        {saved && <Alert tone="success">{t('settings.saved')}</Alert>}
        <Button type="submit" variant="primary" disabled={mutation.isPending}>
          {t('settings.save')}
        </Button>
      </form>
    </Card>
  )
}
