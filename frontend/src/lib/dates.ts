/**
 * Dates shown to the user, always in the UI language: without the locale,
 * `toLocaleString()` uses the browser's, and a German UI showed US dates.
 */
export function formatDateTime(value: string | number | Date, locale: string | undefined): string {
  return new Date(value).toLocaleString(locale)
}

export function formatDate(value: string | number | Date, locale: string | undefined): string {
  return new Date(value).toLocaleDateString(locale)
}

export function formatTime(value: string | number | Date, locale: string | undefined): string {
  return new Date(value).toLocaleTimeString(locale)
}

/**
 * <input type="datetime-local"> yields LOCAL time without zone or seconds
 * ("2026-07-09T14:30"); the backend expects RFC3339 with a zone
 * (time.Parse(time.RFC3339, ...)) and rejects anything else with a 400.
 * new Date(v) interprets that string as the browser's local time and
 * toISOString() serialises it in UTC with "Z". The value comes from the URL,
 * so an unparseable one is dropped instead of reaching toISOString(), which
 * throws on an invalid date.
 */
export function isoFromDatetimeLocal(value: string): string | undefined {
  if (!value) return undefined
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}
