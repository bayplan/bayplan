import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation } from '@tanstack/react-query'
import { Copy } from 'lucide-react'
import * as settingsApi from '../../api/settings'
import { Alert, Button, Card, Checkbox, Field, Input, Select } from '../ui'
import { useCopy } from '../../hooks/useCopy'
import { apiErrorText } from '../../lib/errors'

type Preset = 'generic' | 'entra' | 'keycloak' | 'google' | 'okta'

// Settings that change when a preset is chosen. They only fill the form:
// what gets saved is always the resulting generic configuration, so a preset
// is a shortcut and not a separate backend mode.
const PRESETS: Record<Preset, Partial<settingsApi.OIDCSettingsPayload>> = {
  generic: {},
  entra: {
    displayName: 'Microsoft Entra ID',
    issuer: 'https://login.microsoftonline.com/<tenant-id>/v2.0',
    // The Graph scope is what enables the groups fallback (overage).
    scopes: 'openid profile email https://graph.microsoft.com/GroupMember.Read.All',
    groupsClaim: 'groups',
    usernameClaim: 'preferred_username',
    graphGroupsFallback: true,
  },
  keycloak: {
    displayName: 'Keycloak',
    issuer: 'https://<keycloak>/realms/<realm>',
    scopes: 'openid profile email groups',
    groupsClaim: 'groups',
    usernameClaim: 'preferred_username',
    graphGroupsFallback: false,
  },
  google: {
    displayName: 'Google',
    issuer: 'https://accounts.google.com',
    scopes: 'openid profile email',
    groupsClaim: '',
    usernameClaim: 'email',
    graphGroupsFallback: false,
  },
  okta: {
    displayName: 'Okta',
    issuer: 'https://<org>.okta.com',
    scopes: 'openid profile email groups',
    groupsClaim: 'groups',
    usernameClaim: 'preferred_username',
    graphGroupsFallback: false,
  },
}

interface Props {
  initial: settingsApi.OIDCSettings
  publicURL: string
  onSaved: () => void
}

/**
 * OIDC identity provider. Replaces the four AZURE_* variables: before, only
 * Entra ID fit and any change required a redeploy; now any OIDC IdP is
 * configured here and applied hot.
 */
export function OIDCSettingsCard({ initial, publicURL, onSaved }: Props) {
  const { t } = useTranslation()
  const [form, setForm] = useState<settingsApi.OIDCSettingsPayload>(toPayload(initial))
  const [clientSecret, setClientSecret] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const { copy, copied } = useCopy()

  useEffect(() => {
    setForm(toPayload(initial))
    setClientSecret('')
  }, [initial])

  const mutation = useMutation({
    mutationFn: settingsApi.updateOIDC,
    onSuccess: () => {
      setError(null)
      setSaved(true)
      setClientSecret('')
      onSaved()
    },
    onError: (err) => {
      setSaved(false)
      setError(apiErrorText(err, t('settings.saveError')))
    },
  })

  function set<K extends keyof settingsApi.OIDCSettingsPayload>(key: K, value: settingsApi.OIDCSettingsPayload[K]) {
    setForm((f) => ({ ...f, [key]: value }))
  }

  function applyPreset(preset: Preset) {
    setForm((f) => ({ ...f, ...PRESETS[preset] }))
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSaved(false)
    mutation.mutate({ ...form, ...(clientSecret ? { clientSecret } : {}) })
  }

  // The redirect URI is computed by the backend from the public URL; if there
  // is no public URL yet, the browser's is shown so the admin knows what to
  // register in the provider.
  const redirectURI = initial.redirectURI || `${publicURL || window.location.origin}/api/auth/oidc/callback`

  const copyRedirect = () => copy(redirectURI)

  return (
    <Card className="p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold">{t('settings.oidc.title')}</h2>
          <p className="mt-1 text-xs text-fg-muted">{t('settings.oidc.hint')}</p>
        </div>
        <label className="flex items-center text-xs text-fg-muted">
          {t('settings.oidc.preset')}
          <Select defaultValue="generic" onChange={(e) => applyPreset(e.target.value as Preset)} className="ml-2 h-7 w-auto text-xs">
            <option value="generic">{t('settings.oidc.presetGeneric')}</option>
            <option value="entra">Microsoft Entra ID</option>
            <option value="keycloak">Keycloak</option>
            <option value="google">Google</option>
            <option value="okta">Okta</option>
          </Select>
        </label>
      </div>

      <form onSubmit={handleSubmit} className="mt-4 grid min-w-0 gap-3 md:grid-cols-2 [&>*]:min-w-0">
        <Checkbox label={t('settings.oidc.enabled')} checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} className="md:col-span-2" />

        <Field label={t('settings.oidc.displayName')}>
          <Input value={form.displayName} onChange={(e) => set('displayName', e.target.value)} maxLength={60} placeholder="SSO" />
        </Field>
        <Field label={t('settings.oidc.issuer')}>
          <Input mono value={form.issuer} onChange={(e) => set('issuer', e.target.value)} required={form.enabled} placeholder="https://idp.example.com/realms/main" />
        </Field>
        <Field label={t('settings.oidc.clientId')}>
          <Input mono value={form.clientId} onChange={(e) => set('clientId', e.target.value)} required={form.enabled} />
        </Field>
        <Field label={t('settings.oidc.clientSecret')}>
          <Input
            mono
            type="password"
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            autoComplete="new-password"
            placeholder={initial.clientSecretSet ? t('settings.oidc.secretKept') : ''}
          />
        </Field>
        <Field label={t('settings.oidc.scopes')}>
          <Input mono value={form.scopes} onChange={(e) => set('scopes', e.target.value)} placeholder="openid profile email groups" />
        </Field>
        <Field label={t('settings.oidc.usernameClaim')}>
          <Input mono value={form.usernameClaim} onChange={(e) => set('usernameClaim', e.target.value)} placeholder="preferred_username" />
        </Field>
        <Field label={t('settings.oidc.groupsClaim')} hint={t('settings.oidc.groupsClaimHint')}>
          <Input mono value={form.groupsClaim} onChange={(e) => set('groupsClaim', e.target.value)} placeholder="groups" />
        </Field>
        <Checkbox
          label={t('settings.oidc.graphFallback')}
          hint={t('settings.oidc.graphFallbackHint')}
          checked={form.graphGroupsFallback}
          onChange={(e) => set('graphGroupsFallback', e.target.checked)}
        />

        <div className="min-w-0 md:col-span-2">
          <span className="text-sm">{t('settings.oidc.redirectURI')}</span>
          <div className="mt-1 flex min-w-0 items-center gap-2">
            {/* min-w-0: without it the URL sets the grid's minimum width and the page scrolls horizontally on mobile. */}

            <code className="min-w-0 flex-1 truncate rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs">
              {redirectURI}
            </code>
            <Button type="button" size="sm" onClick={() => void copyRedirect()}>
              <Copy className="h-3.5 w-3.5" aria-hidden="true" />
              {copied ? t('common.copied') : t('common.copy')}
            </Button>
          </div>
          <span className="mt-1 block text-xs text-fg-muted">{t('settings.oidc.redirectURIHint')}</span>
        </div>

        {error && (
          <Alert tone="danger" className="md:col-span-2">
            {error}
          </Alert>
        )}
        {saved && (
          <Alert tone="success" className="md:col-span-2">
            {t('settings.saved')}
          </Alert>
        )}

        <div className="md:col-span-2">
          <Button type="submit" variant="primary" disabled={mutation.isPending}>
            {mutation.isPending ? t('settings.oidc.validating') : t('settings.save')}
          </Button>
        </div>
      </form>
    </Card>
  )
}

function toPayload(s: settingsApi.OIDCSettings): settingsApi.OIDCSettingsPayload {
  return {
    enabled: s.enabled,
    displayName: s.displayName,
    issuer: s.issuer,
    clientId: s.clientId,
    scopes: s.scopes,
    groupsClaim: s.groupsClaim,
    usernameClaim: s.usernameClaim,
    graphGroupsFallback: s.graphGroupsFallback,
  }
}
