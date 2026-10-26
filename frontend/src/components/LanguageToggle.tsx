import { useTranslation } from 'react-i18next'
import { Check, ChevronDown, Languages } from 'lucide-react'
import { LANGUAGE_NAMES, SUPPORTED_LANGUAGES } from '../i18n'
import { usePreferences } from '../hooks/usePreferences'
import { Menu, MenuContent, MenuItem, MenuTrigger } from './ui'

// A dropdown rather than buttons in a row: with three languages the ES/CA/EN
// group already took half the topbar, and every new language made it longer.
// Closed, it only shows the current language code; open, the name of each one
// in its own language.
//
// The language is stored as a user preference on the server, not only in
// i18next's localStorage: that way it travels between browsers. It is
// PreferencesProvider that applies the change to i18next, not this component.

export function LanguageToggle() {
  const { t, i18n } = useTranslation()
  const { update } = usePreferences()
  const current = i18n.resolvedLanguage

  return (
    <Menu>
      <MenuTrigger asChild>
        <button
          type="button"
          aria-label={t('profile.language')}
          title={t('profile.language')}
          className="flex items-center gap-1 rounded-md px-1.5 py-1.5 text-sm text-[var(--toggle-muted,var(--color-fg-muted))] transition-colors hover:bg-[var(--toggle-hover,var(--color-surface-hover))] hover:text-[var(--toggle-text,var(--color-fg))]"
        >
          <Languages aria-hidden="true" />
          <span className="text-xs font-medium uppercase">{current}</span>
          <ChevronDown className="h-3 w-3 opacity-70" aria-hidden="true" />
        </button>
      </MenuTrigger>
      <MenuContent className="min-w-36">
        {SUPPORTED_LANGUAGES.map((lng) => (
          <MenuItem key={lng} lang={lng} onSelect={() => void update({ language: lng })} className="justify-between">
            {LANGUAGE_NAMES[lng]}
            {current === lng && <Check aria-hidden="true" />}
          </MenuItem>
        ))}
      </MenuContent>
    </Menu>
  )
}
