import { api } from '../lib/api'

export type ChannelType = 'webhook' | 'slack' | 'teams' | 'email'

export interface ChannelConfig {
  to?: string[]
  headerName?: string
  insecureSkipVerify?: boolean
}

export interface ChannelSecret {
  url?: string
  headerValue?: string
}

export interface NotificationChannel {
  id: number
  name: string
  type: ChannelType
  config: ChannelConfig
  secretSet: boolean
  enabled: boolean
  createdAt: string
  updatedAt: string
}

export interface ChannelPayload {
  name?: string
  type?: ChannelType
  config?: ChannelConfig
  /** Empty or omitted on edit = keep the current URL. */
  secret?: ChannelSecret
  enabled?: boolean
}

export interface NotificationRule {
  id: number
  name: string
  enabled: boolean
  /** null = all clusters */
  clusterId: number | null
  severities: string[]
  kinds: string[]
  reasons: string[]
  namespaces: string[]
  minDurationSeconds: number
  cooldownSeconds: number
  notifyResolved: boolean
  channelIds: number[]
  createdAt: string
  updatedAt: string
}

export interface RulePayload {
  name?: string
  enabled?: boolean
  clusterId?: number | null
  /** true = remove the cluster filter (null does not travel in JSON as a change). */
  clearCluster?: boolean
  severities?: string[]
  kinds?: string[]
  reasons?: string[]
  namespaces?: string[]
  minDurationSeconds?: number
  cooldownSeconds?: number
  notifyResolved?: boolean
  channelIds?: number[]
}

export type DeliveryEvent = 'firing' | 'resolved' | 'test'

export interface Delivery {
  id: number
  ts: string
  ruleId?: number
  ruleName: string
  channelId?: number
  channelName: string
  cluster?: string
  issueKey?: string
  event: DeliveryEvent
  title: string
  ok: boolean
  error?: string
}

export interface NotificationOptions {
  reasons: string[]
  kinds: string[]
}

const base = '/notifications'

export function getNotificationOptions() {
  return api.get<NotificationOptions>(`${base}/options`).then((r) => r.data)
}
export function listChannels() {
  return api.get<NotificationChannel[]>(`${base}/channels`).then((r) => r.data)
}
export function createChannel(payload: ChannelPayload) {
  return api.post<NotificationChannel>(`${base}/channels`, payload).then((r) => r.data)
}
export function updateChannel(id: number, payload: ChannelPayload) {
  return api.patch<NotificationChannel>(`${base}/channels/${id}`, payload).then((r) => r.data)
}
export function deleteChannel(id: number) {
  return api.delete(`${base}/channels/${id}`)
}
export function testChannel(id: number) {
  return api.post(`${base}/channels/${id}/test`, undefined, { timeout: 20_000 })
}
export function listRules() {
  return api.get<NotificationRule[]>(`${base}/rules`).then((r) => r.data)
}
export function createRule(payload: RulePayload) {
  return api.post<NotificationRule>(`${base}/rules`, payload).then((r) => r.data)
}
export function updateRule(id: number, payload: RulePayload) {
  return api.patch<NotificationRule>(`${base}/rules/${id}`, payload).then((r) => r.data)
}
export function deleteRule(id: number) {
  return api.delete(`${base}/rules/${id}`)
}
export function listDeliveries(limit = 100) {
  return api.get<Delivery[]>(`${base}/deliveries`, { params: { limit } }).then((r) => r.data)
}
