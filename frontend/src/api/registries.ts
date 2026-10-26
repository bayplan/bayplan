import { api } from '../lib/api'

/** Private image registry (platform). The password never comes back. */
export interface ImageRegistry {
  id: number
  name: string
  server: string
  username: string
  email?: string
  insecureSkipTLS: boolean
  /** Name of the Secret created in every bound namespace. */
  secretName: string
  bindingCount: number
  createdAt: string
  updatedAt: string
}

export interface RegistryPayload {
  name?: string
  server?: string
  username?: string
  /** Empty = keep the current one (edit). */
  password?: string
  email?: string
  insecureSkipTLS?: boolean
}

/** Registry distributed to a namespace of a cluster. */
export interface RegistryBinding {
  id: number
  registryId: number
  registryName: string
  server: string
  clusterId: number
  namespace: string
  secretName: string
  attachDefaultSA: boolean
  lastSyncedAt?: string
  lastError?: string
  createdBy?: string
  createdAt: string
}

export function listRegistries() {
  return api.get<ImageRegistry[]>('/registries').then((r) => r.data)
}
export function createRegistry(payload: RegistryPayload) {
  return api.post<ImageRegistry>('/registries', payload).then((r) => r.data)
}
export function updateRegistry(id: number, payload: RegistryPayload) {
  return api.patch<ImageRegistry>(`/registries/${id}`, payload).then((r) => r.data)
}
export function deleteRegistry(id: number) {
  return api.delete<{ secretsRemoved: number; secretsFailed: number }>(`/registries/${id}`).then((r) => r.data)
}
export function listBindings(clusterId: number) {
  return api.get<RegistryBinding[]>(`/clusters/${clusterId}/registries`).then((r) => r.data)
}
export function createBinding(clusterId: number, payload: { registryId: number; namespace: string; attachDefaultSA: boolean }) {
  return api.post<RegistryBinding>(`/clusters/${clusterId}/registries`, payload, { timeout: 40_000 }).then((r) => r.data)
}
export function syncBinding(clusterId: number, id: number) {
  return api.post<RegistryBinding>(`/clusters/${clusterId}/registries/${id}/sync`, undefined, { timeout: 40_000 }).then((r) => r.data)
}
export function deleteBinding(clusterId: number, id: number) {
  return api.delete<{ warning?: string } | ''>(`/clusters/${clusterId}/registries/${id}`, { timeout: 40_000 }).then((r) => r.data)
}
