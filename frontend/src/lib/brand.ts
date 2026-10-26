import { useEffect } from 'react'

// The brand is fixed: the product is called Bayplan and is not customised per
// installation (the "Branding" section of Settings, which allowed changing the
// name and tagline, was removed). The tagline lives in i18n (`login.subtitle`).
export const APP_NAME = 'Bayplan'

/** Sets the tab title with the brand. */
export function useDocumentTitle(suffix?: string) {
  useEffect(() => {
    document.title = suffix ? `${suffix} · ${APP_NAME}` : APP_NAME
  }, [suffix])
}
