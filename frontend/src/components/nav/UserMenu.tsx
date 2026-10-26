import { useTranslation } from 'react-i18next'
import { BookOpen, ChevronDown, KeyRound, LogOut, User as UserIcon } from 'lucide-react'
import { useAuth } from '../../hooks/useAuth'
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from '../ui'

/**
 * Session menu. Besides grouping the logout, it is what makes
 * `/change-password` reachable (it was an orphan route only reached through
 * ProtectedRoute's forced redirect) and the API documentation, which has no
 * place in the sidebar in any case.
 */
export function UserMenu() {
  const { t } = useTranslation()
  const { user, logout } = useAuth()

  if (!user) return null

  return (
    <Menu>
      <MenuTrigger asChild>
        <button
          type="button"
          className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-topbar-text hover:bg-topbar-surface data-[state=open]:bg-topbar-surface"
        >
          <span
            aria-hidden="true"
            className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-topbar-surface text-xs font-semibold uppercase"
          >
            {user.username.slice(0, 1)}
          </span>
          <span className="hidden max-w-32 truncate sm:inline">{user.displayName || user.username}</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 opacity-70" aria-hidden="true" />
        </button>
      </MenuTrigger>
      <MenuContent className="min-w-56">
        <MenuLabel>{user.displayName || user.username}</MenuLabel>
        <MenuItem to="/profile">
          <UserIcon aria-hidden="true" />
          {t('profile.title')}
        </MenuItem>
        {/* SSO users have no local password to change (same rule as the profile page). */}
        {user.source === 'local' && (
          <MenuItem to="/change-password">
            <KeyRound aria-hidden="true" />
            {t('changePassword.title')}
          </MenuItem>
        )}
        <MenuItem to="/api-docs">
          <BookOpen aria-hidden="true" />
          {t('apiDocs.title')}
        </MenuItem>
        <MenuSeparator />
        <MenuItem onSelect={() => void logout()}>
          <LogOut aria-hidden="true" />
          {t('nav.logout')}
        </MenuItem>
      </MenuContent>
    </Menu>
  )
}
