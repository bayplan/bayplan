// Text of an API error to show to the user. The backend responds with the
// reason in the body, either as a plain string (`http.Error`) or as
// `{error: "..."}`; the managed cluster's API server error travels intact as
// a 502 and is the real explanation (RBAC, admission…), so it is shown as is.
// Replaces the 17 local helpers there used to be, with three different
// implementations: one of them ignored the `{error}` form.
export function apiErrorText(err: unknown, fallback: string): string {
  const data = (err as { response?: { data?: unknown } } | undefined)?.response?.data
  if (typeof data === 'string' && data.trim() !== '') return data.trim()
  if (data && typeof data === 'object') {
    const d = data as { error?: unknown; message?: unknown }
    if (typeof d.error === 'string' && d.error !== '') return d.error
    if (typeof d.message === 'string' && d.message !== '') return d.message
  }
  if (err instanceof Error && !('response' in err) && err.message) return err.message
  return fallback
}

/** HTTP status of an axios error, if it got a response. */
export function httpStatus(err: unknown): number | undefined {
  return (err as { response?: { status?: number } } | undefined)?.response?.status
}

/**
 * Reason of a 503 from a read of the informer cache (optional kind without
 * RBAC, cluster not synced): shown as is because it is what the admin needs
 * to read to fix it. `undefined` for any other error.
 */
export function unavailableReason(err: unknown, fallback: string): string | undefined {
  return httpStatus(err) === 503 ? apiErrorText(err, fallback) : undefined
}
