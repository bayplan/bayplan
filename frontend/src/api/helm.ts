import { api } from '../lib/api'
import { namespaceParams as ns } from '../lib/namespaces'

export interface HelmRepo {
  id: number
  name: string
  url: string
  type: 'http' | 'oci'
  username?: string
  hasPassword: boolean
  insecureSkipTLS: boolean
  createdAt: string
}

export interface CreateHelmRepoPayload {
  name: string
  url: string
  username?: string
  password?: string
  insecureSkipTLS?: boolean
}

export function listHelmRepos() {
  return api.get<HelmRepo[]>('/helm/repos').then((r) => r.data)
}

export function createHelmRepo(payload: CreateHelmRepoPayload) {
  return api.post<HelmRepo>('/helm/repos', payload).then((r) => r.data)
}

export function deleteHelmRepo(id: number) {
  return api.delete(`/helm/repos/${id}`)
}

export interface ChartEntry {
  name: string
  description?: string
  version: string
  appVersion?: string
  icon?: string
  home?: string
  keywords?: string[]
  deprecated?: boolean
  /** From newest to oldest. */
  versions: string[]
}

/** OCI repos have no index: `chart` must be passed and its tags are returned. */
export function searchCharts(repoId: number, q: string, opts: { chart?: string; refresh?: boolean } = {}) {
  return api
    .get<ChartEntry[]>(`/helm/repos/${repoId}/charts`, {
      params: { q: q || undefined, chart: opts.chart || undefined, refresh: opts.refresh ? '1' : undefined },
    })
    .then((r) => r.data)
}

export interface ChartInfo {
  name: string
  version: string
  appVersion?: string
  description?: string
  defaultValues: string
  readme?: string
}

export function getChartInfo(repoId: number, chart: string, version?: string) {
  return api
    .get<ChartInfo>(`/helm/repos/${repoId}/charts/${encodeURIComponent(chart)}`, { params: { version: version || undefined } })
    .then((r) => r.data)
}

export interface ReleaseSummary {
  name: string
  namespace: string
  revision: number
  status: string
  chart: string
  chartVersion: string
  appVersion: string
  updated: string
  description?: string
}

export interface ReleaseDetail extends ReleaseSummary {
  notes?: string
  values: string
  manifest: string
  history: ReleaseSummary[]
}

export function listReleases(clusterId: number, namespaces: string[]) {
  return api.get<ReleaseSummary[]>(`/clusters/${clusterId}/helm/releases`, { params: ns(namespaces) }).then((r) => r.data)
}

export function getRelease(clusterId: number, namespace: string, name: string) {
  return api.get<ReleaseDetail>(`/clusters/${clusterId}/helm/releases/${namespace}/${name}`).then((r) => r.data)
}

export interface ReleaseInput {
  repoId: number
  chart: string
  version: string
  name: string
  namespace: string
  values: string
  createNamespace?: boolean
  dryRun?: boolean
}

export interface ReleaseResult {
  release: ReleaseSummary
  /** Dry-run only: rendered manifests. */
  manifest?: string
  dryRun: boolean
}

export function installRelease(clusterId: number, input: ReleaseInput) {
  return api.post<ReleaseResult>(`/clusters/${clusterId}/helm/releases`, input).then((r) => r.data)
}

export function upgradeRelease(clusterId: number, namespace: string, name: string, input: ReleaseInput) {
  return api.put<ReleaseResult>(`/clusters/${clusterId}/helm/releases/${namespace}/${name}`, input).then((r) => r.data)
}

export function rollbackRelease(clusterId: number, namespace: string, name: string, revision: number) {
  return api.post(`/clusters/${clusterId}/helm/releases/${namespace}/${name}/rollback`, { revision })
}

export function uninstallRelease(clusterId: number, namespace: string, name: string) {
  return api.delete(`/clusters/${clusterId}/helm/releases/${namespace}/${name}`)
}
