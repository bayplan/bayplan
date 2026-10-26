import { api } from '../lib/api'

export type CloudProvider = 'aws' | 'azure' | 'gcp'

/** NON-sensitive part of a credential (cloud.Config in the backend). */
export interface CloudConfig {
  accessKeyId?: string
  regions?: string[]
  tenantId?: string
  clientId?: string
  subscriptionId?: string
  projectId?: string
  clientEmail?: string
  /** Identity the provider attributes to the credential (ARN, object id, email). */
  identity?: string
}

/** Sensitive part: only travels towards the server, never comes back. */
export interface CloudSecret {
  secretAccessKey?: string
  clientSecret?: string
  serviceAccountJson?: string
}

export interface CloudCredential {
  id: number
  name: string
  provider: CloudProvider
  config: CloudConfig
  secretSet: boolean
  clusterCount: number
  createdBy?: string
  createdAt: string
  updatedAt: string
}

export interface CloudCredentialPayload {
  name?: string
  provider?: CloudProvider
  config?: CloudConfig
  secret?: CloudSecret
}

/** Reference of a cluster within the provider (cloud.Ref). */
export interface CloudRef {
  name: string
  region?: string
  resourceGroup?: string
  location?: string
  project?: string
}

export interface DiscoveredCluster {
  ref: CloudRef
  name: string
  location: string
  version?: string
  status?: string
  endpoint?: string
  /** No public endpoint: the server will only reach it from inside that network. */
  private: boolean
  /** Already registered in the panel with this credential. */
  clusterId?: number
}

export function listCloudCredentials() {
  return api.get<CloudCredential[]>('/cloud/credentials').then((r) => r.data)
}
export function createCloudCredential(payload: CloudCredentialPayload) {
  return api.post<CloudCredential>('/cloud/credentials', payload).then((r) => r.data)
}
export function updateCloudCredential(id: number, payload: CloudCredentialPayload) {
  return api.patch<CloudCredential>(`/cloud/credentials/${id}`, payload).then((r) => r.data)
}
export function deleteCloudCredential(id: number) {
  return api.delete(`/cloud/credentials/${id}`)
}
/** Lists the provider's clusters; may take a while (one call per region/cluster). */
export function discoverCloudClusters(id: number) {
  return api.get<DiscoveredCluster[]>(`/cloud/credentials/${id}/clusters`, { timeout: 90_000 }).then((r) => r.data)
}
