import { api } from '../lib/api'
import { joinNamespaces } from '../lib/namespaces'

export interface ScanCounts {
  critical: number
  high: number
  medium: number
  low: number
  unknown: number
  fixable: number
}

export interface Vulnerability {
  id: string
  severity: 'CRITICAL' | 'HIGH' | 'MEDIUM' | 'LOW' | 'UNKNOWN' | string
  package: string
  installed: string
  fixed?: string
  title?: string
  url?: string
  target: string
}

export interface ScanRecord {
  image: string
  status: 'queued' | 'running' | 'ok' | 'error'
  scannerVersion?: string
  scannedAt?: string
  counts: ScanCounts
  error?: string
  requestedBy?: string
  updatedAt: string
  report?: { image: string; os?: string; vulnerabilities: Vulnerability[]; counts: ScanCounts }
}

export interface ImageUsage {
  image: string
  digest?: string
  pods: number
  workloads: string[]
  scan?: ScanRecord
}

export interface ScanStatus {
  available: boolean
  version?: string
  queued: number
}

export function getScanStatus() {
  return api.get<ScanStatus>('/scan/status').then((r) => r.data)
}
export function listImages(clusterId: number, namespaces: string[]) {
  const ns = joinNamespaces(namespaces)
  return api.get<ImageUsage[]>(`/clusters/${clusterId}/images`, { params: ns ? { namespace: ns } : undefined }).then((r) => r.data)
}
export function enqueueScan(clusterId: number, body: { image?: string; all?: boolean; force?: boolean }, namespaces: string[] = []) {
  const ns = joinNamespaces(namespaces)
  return api.post<{ requested: number; queued: number }>(`/clusters/${clusterId}/images/scan`, body, { params: ns ? { namespace: ns } : undefined }).then((r) => r.data)
}
export function getImageReport(image: string) {
  return api.get<ScanRecord>('/images/report', { params: { image } }).then((r) => r.data)
}
