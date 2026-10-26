import { api } from '../lib/api'

/** Panel roles, from highest to lowest privilege (see rbac.rank in the backend). */
export const ROLES = ['admin', 'editor', 'readonly'] as const
export type Role = (typeof ROLES)[number]

export interface RoleAssignment {
  id: number
  role: Role
  scopeType: 'global' | 'cluster' | 'namespace'
  scopeRef?: string
  clusterName?: string
}

export interface UserWithRoles {
  id: number
  username: string
  source: 'local' | 'oidc'
  disabled: boolean
  roles: RoleAssignment[]
}

/** Where the effective role on a cluster comes from; `both` = both have to be revoked. */
export type AccessSource = 'global' | 'cluster' | 'both'

export interface ClusterAccess {
  clusterId: number
  cluster: string
  /** Empty = no access to that cluster. */
  role?: Role
  source?: AccessSource
}

export interface UserAccess {
  userId: number
  username: string
  globalRole?: string
  clusters: ClusterAccess[]
}

export function getUserAccess(userId: number) {
  return api.get<UserAccess>(`/users/${userId}/access`).then((r) => r.data)
}

export function listUsers() {
  return api.get<UserWithRoles[]>('/users').then((r) => r.data)
}

export interface CreateUserPayload {
  username: string
  password: string
}

export function createUser(payload: CreateUserPayload) {
  return api.post<UserWithRoles>('/users', payload).then((r) => r.data)
}

export interface GrantRolePayload {
  role: Role
  scopeType: 'global' | 'cluster'
  scopeRef?: string
}

export function grantRole(userId: number, payload: GrantRolePayload) {
  return api.post(`/users/${userId}/roles`, payload)
}

export function revokeRole(userId: number, assignmentId: number) {
  return api.delete(`/users/${userId}/roles/${assignmentId}`)
}

// Physical and irreversible deletion, local users only (the backend rejects
// Entra ones: they are recreated on their next SSO login).
export function deleteUser(id: number) {
  return api.delete(`/users/${id}`)
}

export function setUserDisabled(id: number, disabled: boolean) {
  return api.patch(`/users/${id}`, { disabled })
}
