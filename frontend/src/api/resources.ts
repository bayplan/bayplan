import { api } from '../lib/api'
import { namespaceParams as ns } from '../lib/namespaces'

/**
 * Namespace with its creation date. The date is what lets one find the team's
 * namespaces without classifying anything: the ones created by the cluster
 * deployment are all born at once, the team's ones much later.
 */
export interface NamespaceSummary {
  name: string
  createdAt: string
}

export interface PodSummary {
  name: string
  namespace: string
  status: string
  ready: string
  restarts: number
  node: string
  createdAt: string
  containers: string[]
}

export interface DeploymentSummary {
  name: string
  namespace: string
  replicas: number
  readyReplicas: number
  updatedReplicas: number
  availableReplicas: number
  createdAt: string
}

export interface StatefulSetSummary {
  name: string
  namespace: string
  replicas: number
  readyReplicas: number
  createdAt: string
}

/** DaemonSets have no desired replicas: they have one pod per eligible node. */
export interface DaemonSetSummary {
  name: string
  namespace: string
  desired: number
  ready: number
  available: number
  misscheduled: number
  createdAt: string
}

export interface ServiceSummary {
  name: string
  namespace: string
  type: string
  clusterIP: string
  ports: string[]
  createdAt: string
}

export interface NodeSummary {
  name: string
  status: string
  roles: string[]
  version: string
  /** Cordoned: spec.unschedulable. */
  unschedulable: boolean
  createdAt: string
}

export interface EventSummary {
  namespace: string
  type: string
  reason: string
  message: string
  involvedObject: string
  count: number
  lastSeen: string
}

export interface EventFilter {
  type?: string // 'Normal' | 'Warning'
  since?: string // ISO 8601 / RFC3339
  until?: string
}

export interface EventsResponse {
  items: EventSummary[]
  truncated: boolean
}

export interface ClusterSummary {
  pods: number
  podsNotReady: number
  deployments: number
  deploymentsNotReady: number
  nodes: number
  nodesNotReady: number
}

export function getClusterSummary(clusterId: number) {
  return api.get<ClusterSummary>(`/clusters/${clusterId}/summary`).then((r) => r.data)
}

/** Severity of an overview issue. Only two levels, see k8s/attention.go. */
export type IssueSeverity = 'error' | 'warning'

export interface Issue {
  severity: IssueSeverity
  kind: string
  namespace?: string
  name: string
  /** Stable code (CrashLoopBackOff, RolloutIncomplete…), not human-readable text. */
  reason: string
  detail?: string
  since: string
}

export interface ResourceCount {
  total: number
  warning: number
  error: number
}

/**
 * RESERVED capacity (sum of requests) against the nodes' allocatable. It is not
 * real consumption: without metrics-server that cannot be known, and confusing
 * the two leads to wrong conclusions in both directions.
 */
export interface Capacity {
  podsUsed: number
  podsAllocatable: number
  cpuRequestedMilli: number
  cpuAllocatableMilli: number
  memoryRequestedBytes: number
  memoryAllocatableBytes: number
}

export interface ClusterOverview {
  kubeletVersion: string
  /** Only present if the nodes do NOT all have the same version. */
  kubeletVersions?: string[]
  namespaces: number
  counts: Record<'pods' | 'deployments' | 'statefulsets' | 'daemonsets' | 'nodes', ResourceCount>
  capacity: Capacity
  issues: Issue[]
  /** Real total before trimming the list: never truncated silently. */
  issuesTotal: number
  warningEvents: EventSummary[]
  oldestNodeCreatedAt?: string
}

export function getClusterOverview(clusterId: number) {
  return api.get<ClusterOverview>(`/clusters/${clusterId}/overview`).then((r) => r.data)
}

export function listNamespaces(clusterId: number) {
  return api.get<NamespaceSummary[]>(`/clusters/${clusterId}/namespaces`).then((r) => r.data)
}

export function listPods(clusterId: number, namespaces: string[]) {
  return api
    .get<PodSummary[]>(`/clusters/${clusterId}/pods`, { params: ns(namespaces) })
    .then((r) => r.data)
}

export function listStatefulSets(clusterId: number, namespaces: string[]) {
  return api
    .get<StatefulSetSummary[]>(`/clusters/${clusterId}/statefulsets`, { params: ns(namespaces) })
    .then((r) => r.data)
}

export function listDaemonSets(clusterId: number, namespaces: string[]) {
  return api
    .get<DaemonSetSummary[]>(`/clusters/${clusterId}/daemonsets`, { params: ns(namespaces) })
    .then((r) => r.data)
}

/** Pods of a deployment, resolved in the backend via its ReplicaSets. */
export function listDeploymentPods(clusterId: number, namespace: string, name: string) {
  return api
    .get<PodSummary[]>(`/clusters/${clusterId}/deployments/${namespace}/${name}/pods`)
    .then((r) => r.data)
}

export function listDeployments(clusterId: number, namespaces: string[]) {
  return api
    .get<DeploymentSummary[]>(`/clusters/${clusterId}/deployments`, { params: ns(namespaces) })
    .then((r) => r.data)
}

export function listServices(clusterId: number, namespaces: string[]) {
  return api
    .get<ServiceSummary[]>(`/clusters/${clusterId}/services`, { params: ns(namespaces) })
    .then((r) => r.data)
}

export function listNodes(clusterId: number) {
  return api.get<NodeSummary[]>(`/clusters/${clusterId}/nodes`).then((r) => r.data)
}

export function listEvents(clusterId: number, namespaces: string[], filter: EventFilter = {}) {
  return api
    .get<EventsResponse>(`/clusters/${clusterId}/events`, {
      params: { ...ns(namespaces), ...filter },
    })
    .then((r) => r.data)
}

// --- Writes (editor role; delete and drain, admin) ---

export type RestartableKind = 'deployments' | 'statefulsets' | 'daemonsets'
export type ScalableKind = 'deployments' | 'statefulsets'

export function restartWorkload(clusterId: number, kind: RestartableKind, namespace: string, name: string) {
  return api.post(`/clusters/${clusterId}/${kind}/${namespace}/${name}/restart`)
}

export function scaleWorkload(clusterId: number, kind: ScalableKind, namespace: string, name: string, replicas: number) {
  return api.post(`/clusters/${clusterId}/${kind}/${namespace}/${name}/scale`, { replicas })
}

export function rollbackDeployment(clusterId: number, namespace: string, name: string) {
  return api.post<{ revision: number }>(`/clusters/${clusterId}/deployments/${namespace}/${name}/rollback`).then((r) => r.data)
}

export function cordonNode(clusterId: number, name: string, cordon: boolean) {
  return api.post(`/clusters/${clusterId}/nodes/${name}/cordon`, { cordon })
}

export interface DrainResult {
  evicted: string[]
  skipped: string[]
  failed: string[]
}

export function drainNode(clusterId: number, name: string) {
  return api.post<DrainResult>(`/clusters/${clusterId}/nodes/${name}/drain`).then((r) => r.data)
}

export function deleteResource(clusterId: number, kind: YamlKind, namespace: string | undefined, name: string) {
  const nsSegment = CLUSTER_SCOPED_KINDS.has(kind) || !namespace ? '-' : namespace
  return api.delete(`/clusters/${clusterId}/resources/${kind}/${nsSegment}/${name}`)
}

export interface ApplyResult {
  kind: string
  apiVersion: string
  namespace?: string
  name: string
  operation: 'created' | 'configured'
}

export interface ApplyResponse {
  applied: ApplyResult[]
  dryRun: boolean
}

/**
 * Server-side apply of the YAML (one or several documents). With dryRun the
 * API server validates without persisting; the UI uses it as a mandatory
 * previous step.
 */
export function applyYAML(clusterId: number, yaml: string, namespace: string, dryRun: boolean) {
  return api.post<ApplyResponse>(`/clusters/${clusterId}/apply`, { yaml, namespace, dryRun }).then((r) => r.data)
}

// --- Secondary kinds (optional informers in the backend) ---

export interface ConfigMapSummary {
  name: string
  namespace: string
  keys: string[]
  createdAt: string
}
/** Metadata only: the values never travel in the listing. */
export interface SecretSummary {
  name: string
  namespace: string
  type: string
  keys: string[]
  createdAt: string
}
export interface ServiceAccountSummary {
  name: string
  namespace: string
  secrets: number
  createdAt: string
}
export interface IngressSummary {
  name: string
  namespace: string
  class: string
  hosts: string[]
  paths: string[]
  tls: boolean
  addresses: string[]
  createdAt: string
}
export interface NetworkPolicySummary {
  name: string
  namespace: string
  podSelector: string
  policyTypes: string[]
  createdAt: string
}
export interface PVCSummary {
  name: string
  namespace: string
  status: string
  volume: string
  capacity: string
  accessModes: string[]
  storageClass: string
  createdAt: string
}
export interface PVSummary {
  name: string
  status: string
  capacity: string
  accessModes: string[]
  reclaimPolicy: string
  storageClass: string
  claim: string
  createdAt: string
}
export interface StorageClassSummary {
  name: string
  provisioner: string
  reclaimPolicy: string
  volumeBindingMode: string
  isDefault: boolean
  createdAt: string
}
export interface JobSummary {
  name: string
  namespace: string
  status: string
  succeeded: number
  failed: number
  active: number
  completions: number
  duration?: string
  startedAt?: string
  createdAt: string
}
export interface CronJobSummary {
  name: string
  namespace: string
  schedule: string
  suspended: boolean
  active: number
  lastSchedule?: string
  createdAt: string
}
export interface HPASummary {
  name: string
  namespace: string
  target: string
  minReplicas: number
  maxReplicas: number
  current: number
  desired: number
  metrics: string[]
  createdAt: string
}

export interface QuotaResource {
  name: string
  hard: string
  used: string
  /** used/hard in %; -1 if it cannot be computed. */
  percent: number
}
export interface ResourceQuotaSummary {
  name: string
  namespace: string
  scopes: string[]
  resources: QuotaResource[]
  /** Created by the panel's quota form (bayplan-quota). */
  managedByPanel: boolean
  createdAt: string
}
export interface LimitRangeItem {
  type: string
  resource: string
  min?: string
  max?: string
  default?: string
  defaultRequest?: string
}
export interface LimitRangeSummary {
  name: string
  namespace: string
  limits: LimitRangeItem[]
  managedByPanel: boolean
  createdAt: string
}

/** Namespace quota form: Kubernetes quantities, or empty = do not set. */
export interface NamespaceQuotaPayload {
  createNamespace?: boolean
  requestsCpu?: string
  requestsMemory?: string
  limitsCpu?: string
  limitsMemory?: string
  pods?: string
  persistentVolumeClaims?: string
  requestsStorage?: string
  defaultRequestCpu?: string
  defaultRequestMemory?: string
  defaultLimitCpu?: string
  defaultLimitMemory?: string
  maxCpu?: string
  maxMemory?: string
  dryRun?: boolean
}

export function applyNamespaceQuota(clusterId: number, namespace: string, payload: NamespaceQuotaPayload) {
  return api.post<{ applied: ApplyResult[]; dryRun: boolean; partialDryRun?: boolean }>(`/clusters/${clusterId}/namespaces/${namespace}/quota`, payload, { timeout: 40_000 }).then((r) => r.data)
}

/** Secondary kinds; the value is the API path segment and the tab. */
export const OPTIONAL_KINDS = [
  'configmaps',
  'secrets',
  'serviceaccounts',
  'ingresses',
  'networkpolicies',
  'persistentvolumeclaims',
  'persistentvolumes',
  'storageclasses',
  'jobs',
  'cronjobs',
  'horizontalpodautoscalers',
  'resourcequotas',
  'limitranges',
] as const
export type OptionalKind = (typeof OPTIONAL_KINDS)[number]

/** Type guard over the path segment (user input). */
export function isOptionalKind(tab: string | undefined): tab is OptionalKind {
  return (OPTIONAL_KINDS as readonly string[]).includes(tab ?? '')
}

/** Cluster-scoped kinds: the namespace filter does not apply and the YAML route uses "-". */
export const CLUSTER_SCOPED_KINDS: ReadonlySet<string> = new Set(['nodes', 'persistentvolumes', 'storageclasses', 'namespaces'])

export function listKind<T>(clusterId: number, kind: OptionalKind, namespaces: string[]) {
  const params = CLUSTER_SCOPED_KINDS.has(kind) ? {} : ns(namespaces)
  return api.get<T[]>(`/clusters/${clusterId}/${kind}`, { params }).then((r) => r.data)
}

// "View YAML": any kind cached by the backend. Cluster-scoped kinds carry "-"
// in that path segment.
export type YamlKind =
  | 'pods'
  | 'deployments'
  | 'statefulsets'
  | 'daemonsets'
  | 'services'
  | 'nodes'
  | 'namespaces'
  | OptionalKind

export function getResourceYAML(clusterId: number, kind: YamlKind, namespace: string, name: string) {
  const nsSegment = CLUSTER_SCOPED_KINDS.has(kind) || !namespace ? '-' : namespace
  return api
    .get<{ yaml: string }>(`/clusters/${clusterId}/resources/${kind}/${nsSegment}/${name}/yaml`)
    .then((r) => r.data.yaml)
}
