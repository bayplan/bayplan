import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from '../components/ui'
import { ApiTokensCard } from '../components/ApiTokensCard'
import { useAuth } from '../hooks/useAuth'
import { usePreferences, ROWS_PER_PAGE_OPTIONS } from '../hooks/usePreferences'
import type { Density, Theme } from '../lib/theme'
import type { LandingPage } from '../hooks/usePreferences'
import { LANGUAGE_NAMES, SUPPORTED_LANGUAGES } from '../i18n'
import { apiErrorText } from '../lib/errors'

export function Profile() {
  const { t } = useTranslation()
  const { user, updateProfile } = useAuth()
  const { preferences, update, saving } = usePreferences()

  const [displayName, setDisplayName] = useState('')
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  // The form starts with what is saved, and resyncs if the
  // user changes (e.g. after saving, which returns the updated user).
  useEffect(() => {
    setDisplayName(user?.displayName ?? '')
    setEmail(user?.email ?? '')
  }, [user])

  if (!user) return null
  const isLocal = user.source === 'local'

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSaved(false)
    try {
      await updateProfile({ displayName, email })
      setSaved(true)
    } catch (err) {
      setError(apiErrorText(err, t('profile.saveError')))
    }
  }

  return (
    <div>
      <PageHeader
        title={t('profile.title')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('profile.title') }]}
      />

      <div className="grid gap-6 md:grid-cols-2">
        <Card className="p-5">
          <h2 className="text-sm font-semibold">{t('profile.dataTitle')}</h2>

          <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
            <span className="font-mono">{user.username}</span>
            <Badge tone="neutral">{user.source}</Badge>
            {user.role && <Badge tone={user.role === 'admin' ? 'success' : 'neutral'}>{user.role}</Badge>}
          </div>

          {/* An SSO user's data is sent by the IdP on every login, so
              offering it as editable would give a field that overwrites
              itself on the next sign-in. Same criterion as Grafana. */}
          {!isLocal && (
            <p className="mt-3 rounded-md bg-neutral-soft px-3 py-2 text-xs text-fg-muted">
              {t('profile.ssoManaged')}
            </p>
          )}

          <form onSubmit={handleSubmit} className="mt-4 space-y-3">
            <Field label={t('profile.displayName')}>
              <Input
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                disabled={!isLocal}
                maxLength={200}
              />
            </Field>
            <Field label={t('profile.email')}>
              <Input
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                disabled={!isLocal}
                maxLength={200}
              />
            </Field>

            {error && <Alert tone="danger">{error}</Alert>}
            {saved && <Alert tone="success">{t('profile.saved')}</Alert>}

            {isLocal && (
              <Button type="submit" variant="primary">
                {t('profile.save')}
              </Button>
            )}
          </form>

          {isLocal && (
            <p className="mt-4 border-t border-border pt-3 text-sm">
              <Link to="/change-password" className="text-link hover:underline">
                {t('changePassword.title')}
              </Link>
            </p>
          )}
        </Card>

        <Card className="p-5">
          <div className="flex items-baseline justify-between gap-2">
            <h2 className="text-sm font-semibold">{t('profile.preferencesTitle')}</h2>
            {/* They are saved on change, without a button: they are interface settings and the
                effect is immediate and visible, so a "save" is superfluous. */}
            <span className="text-xs text-fg-muted">
              {saving ? t('profile.saving') : t('profile.autoSaved')}
            </span>
          </div>

          <div className="mt-4 space-y-3">
            <Field label={t('profile.theme')}>
              <Select value={preferences.theme} onChange={(e) => void update({ theme: e.target.value as Theme })}>
                <option value="light">{t('theme.light')}</option>
                <option value="dark">{t('theme.dark')}</option>
                <option value="system">{t('theme.system')}</option>
              </Select>
            </Field>

            <Field label={t('profile.language')}>
              <Select value={preferences.language} onChange={(e) => void update({ language: e.target.value })}>
                {SUPPORTED_LANGUAGES.map((lng) => (
                  <option key={lng} value={lng}>
                    {LANGUAGE_NAMES[lng]}
                  </option>
                ))}
              </Select>
            </Field>

            <Field label={t('profile.density')}>
              <Select value={preferences.density} onChange={(e) => void update({ density: e.target.value as Density })}>
                <option value="comfortable">{t('profile.densityComfortable')}</option>
                <option value="compact">{t('profile.densityCompact')}</option>
              </Select>
            </Field>

            <Field label={t('profile.rowsPerPage')}>
              <Select value={preferences.rowsPerPage} onChange={(e) => void update({ rowsPerPage: Number(e.target.value) })}>
                {ROWS_PER_PAGE_OPTIONS.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </Select>
            </Field>

            <Field label={t('profile.landingPage')}>
              <Select value={preferences.landingPage} onChange={(e) => void update({ landingPage: e.target.value as LandingPage })}>
                <option value="dashboard">{t('nav.dashboard')}</option>
                <option value="fleet">{t('fleet.title')}</option>
              </Select>
            </Field>
          </div>

          <p className="mt-4 text-xs text-fg-muted">{t('profile.preferencesHint')}</p>
        </Card>

        <div className="md:col-span-2">
          <ApiTokensCard />
        </div>
      </div>
    </div>
  )
}
