import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import i18n, { SUPPORTED_LANGUAGES } from '../i18n'
import { updatePreferences } from '../api/auth'
import {
  applyDensity,
  applyTheme,
  cachedDensity,
  cachedTheme,
  watchSystemTheme,
  type Density,
  type Theme,
} from '../lib/theme'
import { useAuth } from './useAuth'

/** Page landed on after logging in. */
export type LandingPage = 'dashboard' | 'fleet'

export interface Preferences {
  theme: Theme
  language: string
  density: Density
  rowsPerPage: number
  landingPage: LandingPage
  /**
   * Namespace selection per cluster, indexed by cluster id as text. Per
   * cluster and not global because each one has its own: carrying over
   * another's selection would leave an empty list for no apparent reason.
   */
  namespacesByCluster: Record<string, string[]>
  /** Sidebar reduced to icons (desktop only). */
  sidebarCollapsed: boolean
  /** Folded sidebar sections, by their stable key (`workloads`…). */
  navCollapsed: string[]
  /** Height in px the user dragged the AI assistant panel to (0 = default). */
  assistantHeight: number
}

const DEFAULTS: Preferences = {
  // Light by default, the user's decision; 'system' stays one click away.
  theme: 'light',
  language: 'en',
  density: 'comfortable',
  rowsPerPage: 50,
  landingPage: 'dashboard',
  namespacesByCluster: {},
  sidebarCollapsed: false,
  navCollapsed: [],
  assistantHeight: 0,
}

const ROWS_PER_PAGE_OPTIONS = [25, 50, 100, 200]

interface PreferencesContextValue {
  preferences: Preferences
  /** Saves only what changes: the backend does a partial merge of the JSON. */
  update: (patch: Partial<Preferences>) => Promise<void>
  saving: boolean
}

const PreferencesContext = createContext<PreferencesContextValue | null>(null)

/**
 * Normalizes whatever comes from the server. The document is free-form JSON
 * (the backend deliberately does not interpret it), so it may carry unknown
 * keys, values from a previous UI version or garbage written by hand via SQL:
 * each field falls back to its default if invalid, instead of breaking the
 * application.
 */
function normalize(raw: unknown): Preferences {
  const p = (raw ?? {}) as Record<string, unknown>
  return {
    theme: p.theme === 'light' || p.theme === 'dark' || p.theme === 'system' ? p.theme : DEFAULTS.theme,
    language: typeof p.language === 'string' && (SUPPORTED_LANGUAGES as readonly string[]).includes(p.language) ? p.language : DEFAULTS.language,
    density: p.density === 'compact' ? 'compact' : DEFAULTS.density,
    rowsPerPage:
      typeof p.rowsPerPage === 'number' && ROWS_PER_PAGE_OPTIONS.includes(p.rowsPerPage)
        ? p.rowsPerPage
        : DEFAULTS.rowsPerPage,
    landingPage: p.landingPage === 'fleet' ? 'fleet' : DEFAULTS.landingPage,
    namespacesByCluster: normalizeNamespaceSelection(p.namespacesByCluster),
    sidebarCollapsed: p.sidebarCollapsed === true,
    navCollapsed: Array.isArray(p.navCollapsed) ? p.navCollapsed.filter((s): s is string => typeof s === 'string') : [],
    assistantHeight: typeof p.assistantHeight === 'number' && p.assistantHeight >= 120 && p.assistantHeight <= 4000 ? Math.round(p.assistantHeight) : 0,
  }
}

/**
 * Validated element by element because the document is free-form JSON: it
 * could carry a string where an array is expected, or numbers instead of
 * names. Any entry that does not fit is discarded instead of propagating
 * garbage to the URL.
 */
function normalizeNamespaceSelection(raw: unknown): Record<string, string[]> {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
  const out: Record<string, string[]> = {}
  for (const [clusterId, value] of Object.entries(raw as Record<string, unknown>)) {
    if (!Array.isArray(value)) continue
    const names = value.filter((n): n is string => typeof n === 'string' && n.trim() !== '')
    if (names.length > 0) out[clusterId] = names
  }
  return out
}

/**
 * Language currently applied to i18next, if it is one of ours. Before login it
 * is the default or what the visitor picked on the login screen (the effect
 * below forces the preference onto i18next), not a guess from the browser.
 */
function localLanguage(): string | undefined {
  const current = i18n.resolvedLanguage ?? i18n.language
  return current && (SUPPORTED_LANGUAGES as readonly string[]).includes(current) ? current : undefined
}

export function PreferencesProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth()
  // Initial state from the local cache so as not to flicker before
  // /api/auth/me responds; as soon as the user arrives, the server rules.
  const [preferences, setPreferences] = useState<Preferences>({
    ...DEFAULTS,
    theme: cachedTheme(),
    density: cachedDensity(),
  })
  const [saving, setSaving] = useState(false)

  // Syncs with what the server says every time the user changes (login,
  // logout, password change).
  //
  // Language and theme are the two settings the login screen offers. If the
  // server document does not have them yet (first login), the ones picked
  // before logging in are kept and persisted once instead of being replaced
  // by the defaults, which silently undid the visitor's choice.
  useEffect(() => {
    if (!user) return
    const raw = (user.preferences ?? {}) as Record<string, unknown>
    const server = normalize(raw)
    const carried: Partial<Preferences> = {}
    if (raw.language === undefined) {
      const current = localLanguage()
      if (current) carried.language = current
    }
    if (raw.theme === undefined) carried.theme = cachedTheme()
    setPreferences({ ...server, ...carried })
    if (Object.keys(carried).length > 0) {
      // Best effort: if it fails, the next login tries again.
      updatePreferences(carried).catch(() => {})
    }
  }, [user])

  // Effects: theme and density to the DOM, language to i18next.
  useEffect(() => {
    applyTheme(preferences.theme)
    applyDensity(preferences.density)
    if (i18n.resolvedLanguage !== preferences.language) {
      void i18n.changeLanguage(preferences.language)
    }
  }, [preferences])

  // With the `system` theme we must repaint if the user changes the operating
  // system's dark mode while the application is open.
  useEffect(() => {
    if (preferences.theme !== 'system') return
    return watchSystemTheme(() => applyTheme('system'))
  }, [preferences.theme])

  const update = useCallback(
    async (patch: Partial<Preferences>) => {
      // Optimistic: the interface reacts instantly and if the request fails it
      // recovers by itself on the next /api/auth/me. A UI setting does not
      // deserve blocking the UI waiting for the server.
      setPreferences((prev) => ({ ...prev, ...patch }))
      // Without a session (login and setup screens) there is nowhere to persist
      // on the server: it stays in the local cache, which is the only thing
      // that makes sense for an anonymous visitor. Without this guard, the
      // login's theme toggle would fire a PATCH that responds 401.
      if (!user) return
      setSaving(true)
      try {
        await updatePreferences(patch)
      } finally {
        setSaving(false)
      }
    },
    [user],
  )

  const value = useMemo(() => ({ preferences, update, saving }), [preferences, update, saving])
  return <PreferencesContext.Provider value={value}>{children}</PreferencesContext.Provider>
}

export function usePreferences(): PreferencesContextValue {
  const ctx = useContext(PreferencesContext)
  if (!ctx) throw new Error('usePreferences must be used inside <PreferencesProvider>')

  return ctx
}

export { ROWS_PER_PAGE_OPTIONS }
