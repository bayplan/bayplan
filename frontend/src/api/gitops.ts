import { api } from '../lib/api'
import type { ApplyResult } from './resources'

export interface GitSource {
  id: number
  clusterId: number
  name: string
  repoUrl: string
  branch: string
  path: string
  namespace?: string
  username?: string
  hasToken: boolean
  authType: 'https' | 'ssh'
  hasHostKey: boolean
  prune: boolean
  kustomize: boolean
  intervalSeconds: number
  enabled: boolean
  lastSyncAt?: string
  lastCommit?: string
  lastStatus: 'pending' | 'synced' | 'error'
  lastError?: string
  lastApplied?: ApplyResult[]
  lastPruned?: { kind: string; namespace?: string; name: string }[]
  createdBy?: string
  createdAt: string
}

export interface CreateGitSourcePayload {
  name: string
  repoUrl: string
  branch?: string
  path?: string
  namespace?: string
  username?: string
  token?: string
  intervalSeconds?: number
  authType?: 'https' | 'ssh'
  sshKnownHosts?: string
  prune?: boolean
  kustomize?: boolean
}

/** The webhook token is only returned on creation: afterwards only its hash exists. */
export interface CreatedGitSource extends GitSource {
  webhookToken: string
  webhookPath: string
}

export interface SyncResult {
  commit: string
  applied: ApplyResult[]
  pruned?: { kind: string; namespace?: string; name: string }[]
  skipped: boolean
}

export function listGitSources(clusterId: number) {
  return api.get<GitSource[]>(`/clusters/${clusterId}/gitops`).then((r) => r.data)
}

export function createGitSource(clusterId: number, payload: CreateGitSourcePayload) {
  return api.post<CreatedGitSource>(`/clusters/${clusterId}/gitops`, payload).then((r) => r.data)
}

export function setGitSourceEnabled(clusterId: number, id: number, enabled: boolean) {
  return api.patch<GitSource>(`/clusters/${clusterId}/gitops/${id}`, { enabled }).then((r) => r.data)
}

export function syncGitSource(clusterId: number, id: number) {
  return api.post<SyncResult>(`/clusters/${clusterId}/gitops/${id}/sync`).then((r) => r.data)
}

export function deleteGitSource(clusterId: number, id: number) {
  return api.delete(`/clusters/${clusterId}/gitops/${id}`)
}
