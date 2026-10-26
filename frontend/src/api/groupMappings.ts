import { api } from '../lib/api'
import type { Role } from './users'

export interface GroupMapping {
  id: number
  /** Usually the group's Object ID in Entra, but not always (see backend). */
  adGroupId: string
  role: Role
  scopeType: 'global' | 'cluster'
  scopeRef?: string
  /** Empty if scope_ref points to a cluster that no longer exists (it is TEXT, no FK). */
  clusterName?: string
  createdAt: string
}

export interface CreateGroupMappingPayload {
  adGroupId: string
  role: Role
  scopeType: 'global' | 'cluster'
  scopeRef?: string
}

export function listGroupMappings() {
  return api.get<GroupMapping[]>('/group-mappings').then((r) => r.data)
}

export function createGroupMapping(payload: CreateGroupMappingPayload) {
  return api.post<{ id: number }>('/group-mappings', payload).then((r) => r.data)
}

export function deleteGroupMapping(id: number) {
  return api.delete(`/group-mappings/${id}`)
}
