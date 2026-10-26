import { api } from '../lib/api'
import type { Issue, ResourceCount } from './resources'

/**
 * State of a cluster in the fleet view. `counts` is absent if the cluster is
 * not connected: sending zeros would say "there is nothing" when what is
 * actually happening is that it is unknown.
 */
export interface ClusterAttention {
  clusterId: number
  cluster: string
  connected: boolean
  lastError?: string
  nodes: number
  kubeletVersion?: string
  counts?: Record<'pods' | 'deployments' | 'nodes', ResourceCount>
  issues: Issue[]
  /** Total before trimming to the N worst of that cluster. */
  issuesTotal: number
}

export function getFleetAttention() {
  return api.get<ClusterAttention[]>('/fleet/attention').then((r) => r.data)
}
