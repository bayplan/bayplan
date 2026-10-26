import { api } from '../lib/api'

export interface AuditEntry {
  id: number
  ts: string
  userId?: number
  /** Denormalised username: survives the user being deleted. */
  actorUsername?: string
  ip?: string
  action: string
  cluster?: string
  namespace?: string
  resource?: string
  result: 'success' | 'failure' | 'denied'
  detail?: unknown
}

export interface AuditPage {
  items: AuditEntry[]
  /** "ts,id" of the last row; absent when there are no more pages. */
  nextCursor?: string
}

export interface AuditFilters {
  actor?: string
  action?: string
  cluster?: string
  result?: string
  q?: string
  from?: string
  to?: string
}

function toParams(filters: AuditFilters): URLSearchParams {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(filters)) {
    if (value) params.set(key, value)
  }
  return params
}

export function listAudit(filters: AuditFilters, cursor?: string) {
  const params = toParams(filters)
  if (cursor) params.set('cursor', cursor)
  return api.get<AuditPage>(`/audit?${params.toString()}`).then((r) => r.data)
}

/**
 * CSV download URL. The URL is returned instead of making the request with
 * axios because the download has to be handled by the browser ("save as"
 * dialog), not by code: with axios one would have to build a Blob and a
 * synthetic link to end up in the same place.
 */
export function auditExportUrl(filters: AuditFilters): string {
  return `/api/audit/export?${toParams(filters).toString()}`
}
