import { api } from '../lib/api'
import { joinNamespaces } from '../lib/namespaces'

/** Real consumption of a node next to what is reserved and allocatable (same scale). */
export interface NodeUsage {
  name?: string
  cpuUsedMilli: number
  cpuRequestedMilli: number
  cpuAllocatableMilli: number
  memoryUsedBytes: number
  memoryRequestedBytes: number
  memoryAllocatableBytes: number
  hasMetrics: boolean
}

export interface ClusterUsage {
  /** false = no metrics-server or no permission; `reason` explains it. */
  available: boolean
  /** NotInstalled | Forbidden | Unavailable | Error: the UI only offers to install with NotInstalled. */
  reasonCode?: 'NotInstalled' | 'Forbidden' | 'Unavailable' | 'Error'
  reason?: string
  updatedAt?: string
  totals: NodeUsage
  nodes: NodeUsage[]
}

export interface ContainerUsage {
  name: string
  cpuUsedMilli: number
  cpuRequestMilli: number
  cpuLimitMilli: number
  memoryUsedBytes: number
  memoryRequestBytes: number
  memoryLimitBytes: number
}

export interface PodUsage extends Omit<ContainerUsage, 'name'> {
  namespace: string
  name: string
  node: string
  hasMetrics: boolean
  containers: ContainerUsage[]
}

export interface PodUsageList {
  available: boolean
  reason?: string
  updatedAt?: string
  pods: PodUsage[]
}

export function getClusterUsage(clusterId: number) {
  return api.get<ClusterUsage>(`/clusters/${clusterId}/metrics`).then((r) => r.data)
}

export function getPodUsage(clusterId: number, namespaces: string[] = []) {
  const ns = joinNamespaces(namespaces)
  return api.get<PodUsageList>(`/clusters/${clusterId}/metrics/pods`, { params: ns ? { namespace: ns } : undefined }).then((r) => r.data)
}

/** Unit prices from Settings → Costs. */
export interface CostPrices {
  cpuCoreHour: number
  memoryGiBHour: number
  currency: string
}

export interface NamespaceCost {
  namespace: string
  pods: number
  cpuRequestMilli: number
  memoryRequestBytes: number
  cpuUsedMilli: number
  memoryUsedBytes: number
  reservedMonthly: number
  usedMonthly: number
  billableMonthly: number
}

export interface CostReport {
  available: boolean
  reason?: string
  configured: boolean
  prices: CostPrices
  hoursPerMonth: number
  namespaces: NamespaceCost[]
  totals: NamespaceCost
}

export function getCosts(clusterId: number, namespaces: string[] = []) {
  const ns = joinNamespaces(namespaces)
  return api.get<CostReport>(`/clusters/${clusterId}/costs`, { params: ns ? { namespace: ns } : undefined }).then((r) => r.data)
}

export interface MetricsServerInstallResult {
  applied: { kind: string; namespace?: string; name: string; operation: 'created' | 'configured' }[]
  dryRun: boolean
  partialDryRun?: boolean
  version: string
}

/** Installs (or rehearses with dryRun) the official metrics-server components.yaml with the admin's identity. */
export function installMetricsServer(clusterId: number, payload: { dryRun: boolean; kubeletInsecureTLS: boolean }) {
  return api.post<MetricsServerInstallResult>(`/clusters/${clusterId}/metrics-server`, payload, { timeout: 60_000 }).then((r) => r.data)
}

export function metricsServerManifestUrl(clusterId: number, kubeletInsecureTLS: boolean) {
  return `/api/clusters/${clusterId}/metrics-server/manifest?kubeletInsecureTLS=${kubeletInsecureTLS}`
}
