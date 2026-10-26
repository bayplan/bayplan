import { api } from '../lib/api'
import type { CloudProvider, CloudRef } from './cloud'

/** Connection state the backend keeps in memory (k8s.ClusterStatus). */
export interface ClusterStatus {
  /** The backend has a registered connection (the cluster is enabled). */
  registered: boolean
  /** The last live check against /version responded. */
  connected: boolean
  /** The informer cache has data: reads work. */
  synced: boolean
  serverVersion?: string
  lastError?: string
  lastCheckedAt?: string
}

export type ClusterAuthType = 'token' | 'clientCert' | 'agent' | 'cloud' | 'incluster'
/** incluster = the cluster where the server runs (registered by the chart, shown as `local`). */
export type ClusterConnector = 'direct' | 'agent' | 'cloud' | 'incluster'

/** Credential and reference in the provider (connector=cloud only). */
export interface ClusterCloudInfo {
  credentialId: number
  provider: CloudProvider
  ref: CloudRef
}

export interface Cluster {
  id: number
  displayName: string
  description?: string
  apiEndpoint: string
  enabled: boolean
  authType: ClusterAuthType
  insecureSkipTLSVerify: boolean
  /** direct = the server reaches the API server; agent = outbound tunnel from the cluster. */
  connector: ClusterConnector
  agentLastSeen?: string
  /** scoped | cluster-admin; only means something for agent and cloud. */
  rbacMode: RBACMode
  /** Kubernetes agent only: whether it has applied the RBAC the panel expects today. */
  agentRbac?: { upToDate: boolean; appliedAt?: string; error?: string }
  cloud?: ClusterCloudInfo
  /** kubernetes | docker (Docker/Podman host via agent). */
  kind: 'kubernetes' | 'docker'
  // EFFECTIVE role of the current user on this specific cluster (global +
  // 'cluster' scope combined, see rbac.EffectiveRoleForCluster in the
  // backend). Use it to gate per-cluster write actions (scale/restart/exec)
  // instead of the session's global role.
  role: 'admin' | 'editor' | 'readonly' | ''
  status: ClusterStatus
}

export function listClusters() {
  return api.get<Cluster[]>('/clusters').then((r) => r.data)
}

/**
 * Create/edit. Everything optional on edit: only what changes is sent. A
 * kubeconfig replaces endpoint, CA and credential at once; otherwise they go
 * separately.
 */
export interface ClusterPayload {
  displayName?: string
  description?: string
  apiEndpoint?: string
  caCert?: string
  insecureSkipTLSVerify?: boolean
  saToken?: string
  clientCert?: string
  clientKey?: string
  kubeconfig?: string
  enabled?: boolean
  /** 'agent' on create: no credentials, the agent manifest is generated. */
  connector?: ClusterConnector
  /** 'docker' = Docker/Podman host (always via agent). */
  kind?: 'kubernetes' | 'docker'
  /** connector 'cloud': credential and discovered cluster. */
  cloudCredentialId?: number
  cloudRef?: CloudRef
  /** Permissions the panel generates in the cluster (agent and cloud): scoped or cluster-admin. */
  rbacMode?: RBACMode
}

export type RBACMode = 'scoped' | 'cluster-admin'

/** What creating (and regenerating) an agent cluster returns. */
export interface AgentOnboarding {
  cluster: Cluster
  manifestUrl: string
  command: string
  expiresAt: string
}

/** What creating a cloud cluster returns: the RBAC to apply for the provider identity. */
export interface CloudOnboarding {
  cluster: Cluster
  provider: CloudProvider
  identity: string
  subjectKind: string
  subjectName: string
  rbacYaml: string
}

export type CreateClusterResult = Cluster | AgentOnboarding | CloudOnboarding

export function isAgentOnboarding(v: CreateClusterResult): v is AgentOnboarding {
  return typeof (v as AgentOnboarding).command === 'string'
}
export function isCloudOnboarding(v: CreateClusterResult): v is CloudOnboarding {
  return typeof (v as CloudOnboarding).rbacYaml === 'string'
}

export function createCluster(payload: ClusterPayload) {
  return api.post<CreateClusterResult>('/clusters', payload).then((r) => r.data)
}

export function getCloudRBAC(id: number) {
  return api.get<CloudOnboarding>(`/clusters/${id}/cloud/rbac`).then((r) => r.data)
}

export function regenerateAgentManifest(id: number) {
  return api.post<AgentOnboarding>(`/clusters/${id}/agent/manifest`).then((r) => r.data)
}

export function updateCluster(id: number, payload: ClusterPayload) {
  return api.patch<Cluster>(`/clusters/${id}`, payload).then((r) => r.data)
}

export function setClusterEnabled(id: number, enabled: boolean) {
  return updateCluster(id, { enabled })
}

export function deleteCluster(id: number) {
  return api.delete(`/clusters/${id}`)
}
