import { Navigate, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../hooks/useAuth'
import type { ReactNode } from 'react'

export function ProtectedRoute({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const { user, loading } = useAuth()
  const location = useLocation()

  if (loading) {
    return (
      <div className="flex h-40 items-center justify-center text-fg-muted">
        {t('common.loading')}
      </div>
    )
  }

  // The original location travels in the router state so that Login can come
  // back to it: a deep link (shared resource, notification) used to land on
  // the dashboard after logging in.
  if (!user) return <Navigate to="/login" replace state={{ from: location }} />

  // Password pending change (reset forced by an admin): blocks the rest
  // of the app until /change-password is completed.

  if (user.mustChangePassword && location.pathname !== '/change-password') {
    return <Navigate to="/change-password" replace state={{ from: location }} />
  }

  return <>{children}</>
}
