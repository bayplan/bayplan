/**
 * Returns the URL only if it is absolute http(s); otherwise `undefined`.
 *
 * For links whose target comes from data the panel does not control (a Helm
 * repo's index.yaml, a Trivy report, an extension after placeholder
 * substitution): React does not block `javascript:` in `href`, so a hostile
 * index could run script in the panel's origin with one click.
 */
export function safeHttpUrl(raw: string | undefined | null): string | undefined {
  if (!raw) return undefined
  try {
    const url = new URL(raw.trim())
    return url.protocol === 'http:' || url.protocol === 'https:' ? url.href : undefined
  } catch {
    return undefined
  }
}

/**
 * Where to go back to after login, from the `from` that ProtectedRoute left in
 * the router state. Only a same-origin path is accepted (`/x`, never `//host`
 * nor `/\host`, which browsers read as protocol-relative), and never the auth
 * screens themselves, which would loop.
 */
export function safeReturnPath(state: unknown): string | undefined {
  const from = (state as { from?: { pathname?: unknown; search?: unknown; hash?: unknown } } | null)?.from
  if (!from || typeof from.pathname !== 'string') return undefined
  const path = from.pathname
  if (!path.startsWith('/') || path.startsWith('//') || path.startsWith('/\\')) return undefined
  if (['/login', '/setup', '/change-password'].includes(path)) return undefined
  const search = typeof from.search === 'string' ? from.search : ''
  const hash = typeof from.hash === 'string' ? from.hash : ''
  return path + search + hash
}
