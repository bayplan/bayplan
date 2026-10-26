import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../hooks/useAuth'
import { LanguageToggle } from '../components/LanguageToggle'
import { ThemeToggle } from '../components/ThemeToggle'
import { Brand } from '../components/Brand'
import { Alert, Button, Field, Input, cardClassName } from '../components/ui'
import { apiErrorText } from '../lib/errors'

export function Setup() {
  const { t } = useTranslation()
  const { setup } = useAuth()
  const navigate = useNavigate()

  const [token, setToken] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (newPassword !== confirm) {
      setError(t('setup.mismatch'))
      return
    }
    setSubmitting(true)
    try {
      await setup(token.trim(), newPassword)
      navigate('/', { replace: true })
    } catch (err) {
      setError(apiErrorText(err, t('setup.error')))
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

      <div className={`grid w-full max-w-3xl overflow-hidden md:grid-cols-2 ${cardClassName}`}>
        <div className="flex flex-col justify-center gap-3 bg-topbar p-8 text-topbar-text">
          <Brand size="md" tone="inherit" />
          <p className="text-sm text-topbar-muted">{t('setup.subtitle')}</p>
        </div>

        <div className="p-8">
          <h1 className="text-xl font-semibold text-fg">{t('setup.title')}</h1>

          <form className="mt-6 space-y-4" onSubmit={handleSubmit}>
            <Field label={t('setup.token')}>
              <Input id="token" mono value={token} onChange={(e) => setToken(e.target.value)} required autoComplete="off" />
            </Field>
            <Field label={t('setup.newPassword')}>
              <Input
                id="newPassword"
                type="password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                required
                minLength={12}
                autoComplete="new-password"
              />
            </Field>
            <Field label={t('setup.confirmPassword')}>
              <Input
                id="confirm"
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                required
                minLength={12}
                autoComplete="new-password"
              />
            </Field>

            {error && <Alert tone="danger">{error}</Alert>}

            <Button type="submit" variant="primary" disabled={submitting} className="w-full">
              {t('setup.submit')}
            </Button>
          </form>
        </div>
      </div>
    </div>
  )
}
