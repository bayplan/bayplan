import { api } from '../lib/api'
import type { ApplyResult } from './resources'

export interface BackupTarget {
  id: number
  name: string
  endpoint?: string
  region: string
  bucket: string
  prefix?: string
  accessKeyId: string
  pathStyle: boolean
  insecureSkipTLS: boolean
  scheduleCount: number
  createdAt: string
  updatedAt: string
}

export interface BackupTargetPayload {
  name?: string
  endpoint?: string
  region?: string
  bucket?: string
  prefix?: string
  accessKeyId?: string
  /** Empty = keep (edit). */
  secretAccessKey?: string
  pathStyle?: boolean
  insecureSkipTLS?: boolean
}

export interface BackupSchedule {
  id: number
  name: string
  clusterId: number
  targetId: number
  targetName: string
  namespaces: string[]
  includeSecrets: boolean
  /** 0 = manual only */
  intervalHours: number
  retention: number
  enabled: boolean
  lastRunAt?: string
  createdBy?: string
  createdAt: string
}

export interface BackupSchedulePayload {
  name?: string
  targetId?: number
  namespaces?: string[]
  includeSecrets?: boolean
  intervalHours?: number
  retention?: number
  enabled?: boolean
}

export interface BackupRun {
  id: number
  scheduleId: number
  startedAt: string
  finishedAt?: string
  status: 'running' | 'ok' | 'error'
  trigger: 'schedule' | 'manual'
  objectKey?: string
  sizeBytes: number
  objects: number
  skipped?: Record<string, string>
  error?: string
}

export interface BackupManifestItem {
  kind: string
  namespace?: string
  name: string
  path: string
}
export interface BackupManifest {
  version: number
  cluster: string
  clusterId: number
  schedule: string
  createdAt: string
  includeSecrets: boolean
  items: BackupManifestItem[]
  skipped?: Record<string, string>
}

export function listTargets() {
  return api.get<BackupTarget[]>('/backup/targets').then((r) => r.data)
}
export function createTarget(p: BackupTargetPayload) {
  return api.post<BackupTarget>('/backup/targets', p, { timeout: 90_000 }).then((r) => r.data)
}
export function updateTarget(id: number, p: BackupTargetPayload) {
  return api.patch<BackupTarget>(`/backup/targets/${id}`, p, { timeout: 90_000 }).then((r) => r.data)
}
export function deleteTarget(id: number) {
  return api.delete(`/backup/targets/${id}`)
}
export function listSchedules(clusterId: number) {
  return api.get<BackupSchedule[]>(`/clusters/${clusterId}/backups`).then((r) => r.data)
}
export function createSchedule(clusterId: number, p: BackupSchedulePayload) {
  return api.post<BackupSchedule>(`/clusters/${clusterId}/backups`, p).then((r) => r.data)
}
export function updateSchedule(clusterId: number, id: number, p: BackupSchedulePayload) {
  return api.patch<BackupSchedule>(`/clusters/${clusterId}/backups/${id}`, p).then((r) => r.data)
}
export function deleteSchedule(clusterId: number, id: number) {
  return api.delete(`/clusters/${clusterId}/backups/${id}`)
}
export function runSchedule(clusterId: number, id: number) {
  return api.post<BackupRun>(`/clusters/${clusterId}/backups/${id}/run`, undefined, { timeout: 6 * 60_000 }).then((r) => r.data)
}
export function listRuns(clusterId: number) {
  return api.get<BackupRun[]>(`/clusters/${clusterId}/backups/runs`).then((r) => r.data)
}
export function getRunIndex(clusterId: number, runId: number) {
  return api.get<BackupManifest>(`/clusters/${clusterId}/backups/runs/${runId}/index`, { timeout: 60_000 }).then((r) => r.data)
}
export function downloadUrl(clusterId: number, runId: number) {
  return `/api/clusters/${clusterId}/backups/runs/${runId}/download`
}
export function restoreRun(clusterId: number, runId: number, paths: string[], dryRun: boolean) {
  return api.post<{ applied: ApplyResult[]; dryRun: boolean }>(`/clusters/${clusterId}/backups/runs/${runId}/restore`, { paths, dryRun }, { timeout: 5 * 60_000 }).then((r) => r.data)
}

/** Schedule of the backups of Bayplan's own database (pg_dump to an S3 target). */
export interface ServerBackupConfig {
  enabled: boolean
  targetId: number
  intervalHours: number
  keep: number
}

export interface ServerBackupRun {
  id: number
  trigger: 'schedule' | 'manual'
  targetName: string
  status: 'running' | 'ok' | 'error'
  objectKey?: string
  sizeBytes: number
  error?: string
  startedAt: string
  finishedAt?: string
}

export interface ServerBackupState {
  config: ServerBackupConfig
  runs: ServerBackupRun[]
  /** pg_dump present on the server (it is in the official image). */
  available: boolean
}

export function getServerBackup() {
  return api.get<ServerBackupState>('/admin/server-backup').then((r) => r.data)
}
export function updateServerBackup(c: ServerBackupConfig) {
  return api.put<ServerBackupConfig>('/admin/server-backup', c).then((r) => r.data)
}
export function runServerBackup() {
  return api.post<ServerBackupRun>('/admin/server-backup/run', undefined, { timeout: 31 * 60_000 }).then((r) => r.data)
}
/** A real link, not an XHR: the browser streams the dump to disk. */
export const serverBackupDownloadUrl = '/api/admin/server-backup/download'
