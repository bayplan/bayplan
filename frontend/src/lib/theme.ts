// UI theme. `system` follows the operating system preference and reacts live
// if it changes (see watchSystemTheme): the i18n key theme.system existed from
// the start but the mode did not.
export type Theme = 'light' | 'dark' | 'system'
export type Density = 'comfortable' | 'compact'

// The source of truth for preferences is the server (users.preferences). This
// is only a startup CACHE: without it, between the first render and the
// /api/auth/me response the page would be painted with the default theme and
// flicker. With the browser as the only source there would be no way to
// impose a default value.
const THEME_KEY = 'bayplan-theme'
const DENSITY_KEY = 'bayplan-density'

function systemPrefersDark(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

/** Translates the preference into the theme actually painted. */
export function resolveTheme(theme: Theme): 'light' | 'dark' {
  if (theme === 'system') return systemPrefersDark() ? 'dark' : 'light'
  return theme
}

export function applyTheme(theme: Theme) {
  document.documentElement.classList.toggle('dark', resolveTheme(theme) === 'dark')
  localStorage.setItem(THEME_KEY, theme)
}

export function applyDensity(density: Density) {
  // An attribute on <html> and not a class per table: density affects the whole
  // interface and this way it is resolved with a custom property in index.css,
  // without every component having to know.
  document.documentElement.dataset.density = density
  localStorage.setItem(DENSITY_KEY, density)
}

export function cachedTheme(): Theme {
  const stored = localStorage.getItem(THEME_KEY)
  return stored === 'light' || stored === 'dark' || stored === 'system' ? stored : 'light'
}

export function cachedDensity(): Density {
  return localStorage.getItem(DENSITY_KEY) === 'compact' ? 'compact' : 'comfortable'
}

/**
 * Applies the local cache before the first render (see main.tsx). Whatever the
 * server says is applied afterwards, when /api/auth/me responds.
 */
export function initAppearance() {
  applyTheme(cachedTheme())
  applyDensity(cachedDensity())
}

/**
 * Subscribes to the operating system theme change. Only has an effect while the
 * preference is `system`; returns the function to unsubscribe.
 */
export function watchSystemTheme(onChange: () => void): () => void {
  const media = window.matchMedia('(prefers-color-scheme: dark)')
  media.addEventListener('change', onChange)
  return () => media.removeEventListener('change', onChange)
}
