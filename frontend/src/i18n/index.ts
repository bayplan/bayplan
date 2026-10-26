import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import resourcesToBackend from 'i18next-resources-to-backend'

// Languages supported by Bayplan. English is the source of truth (en.json) and
// the fallback; every other locale must carry the same keys
// (scripts/check-locales.mjs enforces it in `npm run lint`). Persisted in
// localStorage ("i18nextLng", managed by i18next-browser-languagedetector).
export const SUPPORTED_LANGUAGES = ['en', 'es', 'ca', 'de', 'fr', 'pt-BR', 'zh-CN', 'ja'] as const
export type SupportedLanguage = (typeof SUPPORTED_LANGUAGES)[number]

// Each language named in itself, never translated: someone who cannot read the
// current UI language must still recognise their own in the list.
export const LANGUAGE_NAMES: Record<SupportedLanguage, string> = {
  en: 'English',
  es: 'Español',
  ca: 'Català',
  de: 'Deutsch',
  fr: 'Français',
  'pt-BR': 'Português (Brasil)',
  'zh-CN': '简体中文',
  ja: '日本語',
}

// One chunk per language: bundling every JSON into the main bundle cost ~160 KB
// when only one (plus the fallback) is ever used. react-i18next suspends
// rendering until the active language arrives (Suspense in App).
void i18n
  .use(LanguageDetector)
  .use(resourcesToBackend((lng: string) => import(`./locales/${lng}.json`)))
  .use(initReactI18next)
  .init({
    fallbackLng: 'en',
    // Load only the active language and the fallback, not every supported one.
    load: 'currentOnly',
    supportedLngs: SUPPORTED_LANGUAGES,
    interpolation: { escapeValue: false },
    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
    },
  })

export default i18n
