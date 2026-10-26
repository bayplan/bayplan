import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import * as settingsApi from '../../api/settings'
import { apiErrorText } from '../../lib/errors'
import { Alert, Button, Card, Field, Input, Select, Switch } from '../ui'

// Proposed for Anthropic only; for the other families the model names change
// too often and depend on the account or the self-hosted server.
const ANTHROPIC_DEFAULT_MODEL = 'claude-opus-5-5'

/**
 * Provider behind "Explain". One per instance, paid by the organization: the
 * key is write-only (the API says whether one is stored), and "Test" uses
 * what is SAVED so a wrong key or model is found here and not by a user.
 */
export function AISettingsCard({ initial, onSaved }: { initial: settingsApi.AISettings; onSaved: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [form, setForm] = useState(() => toForm(initial))
  const [apiKey, setApiKey] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    setForm(toForm(initial))
    setApiKey('')
  }, [initial])

  const save = useMutation({
    mutationFn: (payload: settingsApi.AISettingsPayload) => settingsApi.updateAI(payload),
    onSuccess: () => {
      setError(null)
      setSaved(true)
      onSaved()
      void queryClient.invalidateQueries({ queryKey: ['ai-status'] })
    },
    onError: (err) => {
      setSaved(false)
      setError(apiErrorText(err, t('settings.saveError')))
    },
  })
  const test = useMutation({ mutationFn: settingsApi.testAI })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSaved(false)
    test.reset()
    save.mutate({ ...form, provider: form.provider, dailyLimitPerUser: Number(form.dailyLimitPerUser) || 0, apiKey: apiKey || undefined })
  }

  const local = form.provider === 'openai'
  return (
    <Card className="p-5">
      <h2 className="text-sm font-semibold">{t('settings.ai.title')}</h2>
      <p className="mt-1 text-xs text-fg-muted">{t('settings.ai.hint')}</p>
      <form onSubmit={handleSubmit} className="mt-4 space-y-3">
        <Switch checked={form.enabled} onCheckedChange={(v) => setForm((f) => ({ ...f, enabled: v }))} label={t('settings.ai.enabled')} />
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('settings.ai.provider')}>
            <Select
              value={form.provider}
              onChange={(e) => {
                const provider = e.target.value as settingsApi.AIProvider | ''
                setForm((f) => ({ ...f, provider, model: provider === 'anthropic' && !f.model ? ANTHROPIC_DEFAULT_MODEL : f.model }))
              }}
            >
              <option value="">{t('settings.ai.choose')}</option>
              <option value="openai">{t('settings.ai.providers.openai')}</option>
              <option value="anthropic">Anthropic (Claude)</option>
              <option value="gemini">Google Gemini</option>
            </Select>
          </Field>
          <Field label={t('settings.ai.model')} hint={form.provider === 'anthropic' ? t('settings.ai.modelHintAnthropic') : t('settings.ai.modelHint')}>
            <Input mono value={form.model} onChange={(e) => setForm((f) => ({ ...f, model: e.target.value }))} placeholder={form.provider === 'anthropic' ? ANTHROPIC_DEFAULT_MODEL : ''} />
          </Field>
        </div>
        {local && (
          <Field label={t('settings.ai.baseURL')} hint={t('settings.ai.baseURLHint')}>
            <Input mono value={form.baseURL} onChange={(e) => setForm((f) => ({ ...f, baseURL: e.target.value }))} placeholder="https://api.openai.com/v1" />
          </Field>
        )}
        <Field label={t('settings.ai.apiKey')} hint={initial.apiKeySet ? t('settings.ai.apiKeySet') : local ? t('settings.ai.apiKeyOptional') : undefined}>
          <Input type="password" autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder={initial.apiKeySet ? '••••••••' : ''} />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('settings.ai.minRole')} hint={t('settings.ai.minRoleHint')}>
            <Select value={form.minRole} onChange={(e) => setForm((f) => ({ ...f, minRole: e.target.value as settingsApi.AISettings['minRole'] }))}>
              <option value="readonly">readonly</option>
              <option value="editor">editor</option>
              <option value="admin">admin</option>
            </Select>
          </Field>
          <Field label={t('settings.ai.dailyLimit')} hint={t('settings.ai.dailyLimitHint')}>
            <Input mono type="number" min={1} max={10000} value={form.dailyLimitPerUser} onChange={(e) => setForm((f) => ({ ...f, dailyLimitPerUser: Number(e.target.value) }))} />
          </Field>
        </div>
        <p className="text-xs text-fg-muted">{t('settings.ai.privacy')}</p>
        {error && <Alert tone="danger">{error}</Alert>}
        {saved && <Alert tone="success">{t('settings.saved')}</Alert>}
        {test.data && (test.data.ok ? <Alert tone="success">{t('settings.ai.testOk', { ms: test.data.latencyMs, reply: test.data.reply })}</Alert> : <Alert tone="danger">{test.data.error}</Alert>)}
        {test.isError && <Alert tone="danger">{apiErrorText(test.error, t('settings.ai.testFailed'))}</Alert>}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t('settings.save')}
          </Button>
          <Button type="button" onClick={() => test.mutate()} loading={test.isPending} disabled={!initial.provider || !initial.model}>
            {t('settings.ai.test')}
          </Button>
          {initial.apiKeySet && (
            <Button type="button" variant="ghost" onClick={() => save.mutate({ ...form, dailyLimitPerUser: Number(form.dailyLimitPerUser) || 0, clearAPIKey: true, enabled: form.enabled && local })}>
              {t('settings.ai.clearKey')}
            </Button>
          )}
        </div>
      </form>
    </Card>
  )
}

function toForm(s: settingsApi.AISettings) {
  return { enabled: s.enabled, provider: s.provider, baseURL: s.baseURL ?? '', model: s.model ?? '', minRole: s.minRole || 'editor', dailyLimitPerUser: s.dailyLimitPerUser || 30 } as Omit<settingsApi.AISettingsPayload, 'apiKey' | 'clearAPIKey'>
}
