import { api } from '../lib/api'
import { namespaceParams } from '../lib/namespaces'

export type TopologyStatus = 'ok' | 'warning' | 'error'
export type TopologyEdgeKind = 'routes' | 'selects' | 'owns' | 'mounts'

export interface TopologyNode {
  /** `Kind/namespace/name`. */
  id: string
  kind: string
  namespace: string
  name: string
  /** Typed as string and not TopologyStatus: a value this UI does not know must still render (as amber). */
  status: string
  /** Stable code (CrashLoopBackOff, NoEndpoints…), translated by the UI. */
  reason?: string
  detail?: string
  /** Workload node id of a pod, used to fold pods into it. */
  owner?: string
  nodeName?: string
  /** `ConfigMap/name` / `Secret/name` referenced by the pod template. */
  config?: string[]
}

export interface TopologyEdge {
  source: string
  target: string
  kind: TopologyEdgeKind
}

export interface Topology {
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  total: number
  /** Too many objects to draw: nodes come back empty and the UI asks for a namespace filter. */
  tooLarge?: boolean
  /** Optional kinds left out (no permission, not synced) → reason. */
  unavailable?: Record<string, string>
}

export function getTopology(clusterId: number, namespaces: string[]) {
  return api.get<Topology>(`/clusters/${clusterId}/topology`, { params: namespaceParams(namespaces) }).then((r) => r.data)
}
