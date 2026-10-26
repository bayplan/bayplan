import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { useAuth } from '../hooks/useAuth'
import { getAuthStatus, goToSsoLogin } from '../api/auth'
import { LanguageToggle } from '../components/LanguageToggle'
import { ThemeToggle } from '../components/ThemeToggle'
import { Brand } from '../components/Brand'
import { safeReturnPath } from '../lib/url'
import { Alert, Button, Field, Input, cardClassName } from '../components/ui'

// Mapping of the failure reasons the backend appends as
// /login?error=<reason> after a failed Entra ID callback, to the corresponding
// i18n key.
const SSO_ERROR_KEYS: Record<string, string> = {
  sso_failed: 'login.errorSso',
  no_role_mapped: 'login.errorNoRole',
  account_disabled: 'login.errorDisabled',
  username_conflict: 'login.errorUsernameConflict',
  invalid_username: 'login.errorInvalidUsername',
}

export function Login() {
  const { t } = useTranslation()
  const { login } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const [searchParams] = useSearchParams()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [ssoEnabled, setSsoEnabled] = useState(false)
  const [ssoName, setSsoName] = useState('')

  useEffect(() => {
    // First boot: until the admin has completed /setup with the
    // one-time token, there is no valid local password to
    // log in with (see plan: an initial password is never generated/logged,
    // only a setup token).
    getAuthStatus()
      .then((status) => {
        if (status.setupPending) navigate('/setup', { replace: true })
        setSsoEnabled(status.ssoEnabled)
        setSsoName(status.ssoDisplayName ?? '')
      })
      .catch(() => {})
  }, [navigate])

  useEffect(() => {
    const ssoError = searchParams.get('error')
    // hasOwn and not a plain lookup: `?error=constructor` would return a
    // function from Object.prototype instead of falling back.
    if (ssoError) setError(Object.hasOwn(SSO_ERROR_KEYS, ssoError) ? SSO_ERROR_KEYS[ssoError] : 'login.errorSso')
  }, [searchParams])

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const user = await login(username, password)
      // Initial view according to the user's preference (landing page). It is read from the login response and not from the
      // preferences context, which in this render still has the previous values.
      // A deep link that sent here wins over the landing page. With a forced
      // password change ProtectedRoute bounces to /change-password and passes
      // it along again.
      const back = safeReturnPath(location.state)
      navigate(back ?? (user?.preferences?.landingPage === 'fleet' ? '/fleet/attention' : '/'), { replace: true })
    } catch {
      setError('login.error')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="flex min-h-full flex-col items-center justify-center gap-8 bg-sidebar px-4 py-8">
      <div className="absolute right-4 top-4 flex gap-2">
        <LanguageToggle />
        <ThemeToggle />
      </div>

      {/* Prism-style split card: brand panel on the left, form on the
          right. On narrow screens the brand panel collapses into a
          compact header. */}
      <div className={`grid w-full max-w-3xl overflow-hidden md:grid-cols-2 ${cardClassName}`}>
        <div className="flex flex-col justify-center gap-3 bg-topbar p-8 text-topbar-text">
          <Brand size="md" tone="inherit" />
          <p className="text-sm text-topbar-muted">{t('login.subtitle')}</p>
        </div>

        <div className="p-8">
          <h1 className="text-xl font-semibold text-fg">{t('login.title')}</h1>

          <form className="mt-6 space-y-4" onSubmit={handleSubmit}>
            <Field label={t('login.username')}>
              <Input
                id="username"
                name="username"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
              />
            </Field>
            <Field label={t('login.password')}>
              <Input
                id="password"
                name="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </Field>

            {error && <Alert tone="danger">{t(error)}</Alert>}

            <Button type="submit" variant="primary" disabled={submitting} className="w-full">
              {t('login.submit')}
            </Button>
          </form>

          <div className="my-4 flex items-center gap-2 text-xs text-fg-muted">
            <div className="h-px flex-1 bg-border" />
            {t('login.or')}
            <div className="h-px flex-1 bg-border" />
          </div>

          <Button
            type="button"
            variant="secondary"
            disabled={!ssoEnabled}
            title={ssoEnabled ? undefined : t('login.ssoComingSoon')}
            onClick={() => goToSsoLogin()}
            className="w-full"
          >
            {t('login.sso', { provider: ssoName || 'SSO' })}
          </Button>
        </div>
      </div>
    </div>
  )
}
