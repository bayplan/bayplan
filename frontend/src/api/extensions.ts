import { api } from '../lib/api'

export type ExtensionMode = 'link' | 'embed'
export type ExtensionRole = 'readonly' | 'editor' | 'admin'

export interface UiExtension {
  id: number
  /** No clusterId = visible in every cluster. */
  clusterId?: number
  name: string
  url: string
  mode: ExtensionMode
  minRole: ExtensionRole
  sortOrder: number
  createdAt: string
  updatedAt: string
}

export interface UiExtensionInput {
  clusterId: number | null
  name: string
  url: string
  mode: ExtensionMode
  minRole: ExtensionRole
  sortOrder: number
}

export function listExtensions() {
  return api.get<UiExtension[]>('/extensions').then((r) => r.data)
}

export function listClusterExtensions(clusterId: number) {
  return api.get<UiExtension[]>(`/clusters/${clusterId}/extensions`).then((r) => r.data)
}

export function createExtension(payload: UiExtensionInput) {
  return api.post<UiExtension>('/extensions', payload).then((r) => r.data)
}

export function updateExtension(id: number, payload: UiExtensionInput) {
  return api.put<UiExtension>(`/extensions/${id}`, payload).then((r) => r.data)
}

export function deleteExtension(id: number) {
  return api.delete(`/extensions/${id}`)
}

/**
 * Replaces the URL placeholders with the current context. Same rule as
 * `extensions.Expand` in the backend: values escaped as a path segment. With
 * an empty namespace filter, {namespace} stays empty: every tool interprets
 * "no namespace" its own way and the panel must not make one up.
 */
export function expandExtensionUrl(raw: string, cluster: { id: number; displayName: string }, namespace: string) {
  return raw
    .replaceAll('{cluster}', encodeURIComponent(cluster.displayName))
    .replaceAll('{clusterId}', String(cluster.id))
    .replaceAll('{namespace}', encodeURIComponent(namespace))
}
