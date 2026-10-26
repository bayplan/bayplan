import { useTranslation } from 'react-i18next'
import { Moon, Sun } from 'lucide-react'
import { resolveTheme } from '../lib/theme'
import { usePreferences } from '../hooks/usePreferences'

// One-click light/dark shortcut. The "system" mode exists, but it is chosen
// from the profile: cycling through three states on an unlabelled button is
// not understandable. If the preference is `system`, this button sets it to
// the opposite of what is being shown at that moment.
//
// The icon shown is the one of the theme it WOULD SWITCH TO when pressed (sun
// = "switch to light", moon = "switch to dark"), not the current theme.

export function ThemeToggle() {
  const { t } = useTranslation()
  const { preferences, update } = usePreferences()

  const isDark = resolveTheme(preferences.theme) === 'dark'
  const Icon = isDark ? Sun : Moon
  const label = t(isDark ? 'theme.switchToLight' : 'theme.switchToDark')

  return (
    <button
      type="button"
      onClick={() => void update({ theme: isDark ? 'light' : 'dark' })}
      aria-label={label}
      title={label}
      className="rounded-md p-1.5 text-[var(--toggle-muted,var(--color-fg-muted))] transition-colors hover:bg-[var(--toggle-hover,var(--color-surface-hover))] hover:text-[var(--toggle-text,var(--color-fg))]"
    >
      <Icon className="h-4 w-4" aria-hidden="true" />
    </button>
  )
}
