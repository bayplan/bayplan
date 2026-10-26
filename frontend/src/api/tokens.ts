import { api } from '../lib/api'

/** Personal API token as listed: never the value. */
export interface APIToken {
  id: number
  name: string
  prefix: string
  readOnly: boolean
  expiresAt?: string
  lastUsedAt?: string
  revokedAt?: string
  createdAt: string
}

export function listTokens() {
  return api.get<APIToken[]>('/auth/tokens').then((r) => r.data)
}
/** The token value only comes in this response. */
export function createToken(payload: { name: string; readOnly: boolean; expiresInDays: number }) {
  return api.post<{ token: string; info: APIToken }>('/auth/tokens', payload).then((r) => r.data)
}
export function revokeToken(id: number) {
  return api.delete<APIToken>(`/auth/tokens/${id}`).then((r) => r.data)
}

export interface OpenAPIOperation {
  tags?: string[]
  summary?: string
  description?: string
  parameters?: { name: string; in: string; required?: boolean; description?: string }[]
  requestBody?: { content?: Record<string, { schema?: unknown }> }
  responses?: Record<string, { description?: string }>
}
export interface OpenAPISpec {
  info: { title: string; version: string; description?: string }
  tags?: { name: string }[]
  paths: Record<string, Record<string, OpenAPIOperation>>
}
export function getOpenAPI() {
  return api.get<OpenAPISpec>('/openapi.json').then((r) => r.data)
}
