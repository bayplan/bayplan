import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../hooks/useAuth'
import { Alert, Button, Card, Field, Input, PageHeader } from '../components/ui'
import { apiErrorText } from '../lib/errors'
import { safeReturnPath } from '../lib/url'

export function ChangePassword() {
  const { t } = useTranslation()
  const { changePassword, user } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()

  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (newPassword !== confirm) {
      setError(t('changePassword.mismatch'))
      return
    }
    setSubmitting(true)
    try {
      await changePassword(currentPassword, newPassword)
      // Back to the deep link that was interrupted by the forced change, if any.
      navigate(safeReturnPath(location.state) ?? '/', { replace: true })
    } catch (err) {
      setError(apiErrorText(err, t('changePassword.error')))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div>
      <PageHeader
        title={t('changePassword.title')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('changePassword.title') }]}
        description={
          user?.mustChangePassword ? (
            <span className="text-warning">{t('changePassword.forced')}</span>
          ) : undefined
        }
      />

      <Card className="max-w-lg p-6">
        <form className="space-y-4" onSubmit={handleSubmit}>
          <Field label={t('changePassword.current')}>
            <Input
              type="password"
              value={currentPassword}
              onChange={(e) => setCurrentPassword(e.target.value)}
              required
              autoComplete="current-password"
            />
          </Field>
          <Field label={t('changePassword.new')}>
            <Input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              required
              minLength={12}
              autoComplete="new-password"
            />
          </Field>
          <Field label={t('changePassword.confirm')}>
            <Input
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
            {t('changePassword.submit')}
          </Button>
        </form>
      </Card>
    </div>
  )
}
