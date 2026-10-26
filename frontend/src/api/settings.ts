import { api } from '../lib/api'

export interface ServerSettings {
  publicURL: string
  /** Agent image written into the onboarding manifest; empty = the default one. */
  agentImage?: string
  /** CA (PEM) the agents trust on top of the system ones; empty = none or the built-in one. */
  agentCACert?: string
}

/**
 * OIDC configuration as the backend returns it: the client secret NEVER
 * travels, only whether it is set. To change it, `clientSecret` is sent with
 * the new value; omitting it keeps the current one.
 */
export interface OIDCSettings {
  enabled: boolean
  displayName: string
  issuer: string
  clientId: string
  clientSecretSet: boolean
  scopes: string
  groupsClaim: string
  usernameClaim: string
  /** If the issuer is Microsoft's, the backend can resolve groups via Graph. */
  graphGroupsFallback: boolean
  /** Redirect URI computed by the backend, to copy into the provider. */
  redirectURI: string
}

export interface OIDCSettingsPayload extends Omit<OIDCSettings, 'clientSecretSet' | 'redirectURI'> {
  clientSecret?: string
}

/** Outgoing mail server for the email notification channels. The password never comes back. */
export interface SMTPSettings {
  host: string
  port: number
  username: string
  passwordSet: boolean
  from: string
  startTLS: boolean
  insecureSkipVerify: boolean
}

export interface SMTPSettingsPayload extends Omit<SMTPSettings, 'passwordSet'> {
  /** Empty = keep the current one. */
  password?: string
}

/** Unit prices for the per-namespace cost estimate (0 = not configured). */
export interface CostSettings {
  cpuCoreHour: number
  memoryGiBHour: number
  currency: string
}

export interface AllSettings {
  server: ServerSettings
  oidc?: OIDCSettings
  smtp: SMTPSettings
  costs: CostSettings
  ai: AISettings
  /** Version of the server binary ("dev" outside a tagged build). */
  serverVersion: string
  /** Agent image used if agentImage is left empty: the one of the same version. */
  defaultAgentImage: string
  /** How the binary serves HTTPS itself: '' (it does not), 'self-signed', 'files' or 'acme'. */
  tlsMode: string
  /** The server already hands the agents the CA of its certificate (built-in self-signed HTTPS or the chart's cert-manager CA). */
  agentCAAuto: boolean
}

export function getSettings() {
  return api.get<AllSettings>('/settings').then((r) => r.data)
}

export function updateServer(payload: ServerSettings) {
  return api.put<ServerSettings>('/settings/server', payload).then((r) => r.data)
}

export function updateOIDC(payload: OIDCSettingsPayload) {
  return api.put<OIDCSettings>('/settings/auth.oidc', payload).then((r) => r.data)
}

export function updateSMTP(payload: SMTPSettingsPayload) {
  return api.put<SMTPSettings>('/settings/smtp', payload).then((r) => r.data)
}

export function updateCosts(payload: CostSettings) {
  return api.put<CostSettings>('/settings/costs', payload).then((r) => r.data)
}

export type AIProvider = 'openai' | 'anthropic' | 'gemini'

/** LLM behind "Explain". The key never comes back: only whether one is stored. */
export interface AISettings {
  enabled: boolean
  provider: AIProvider | ''
  /** OpenAI-compatible servers only; empty = api.openai.com. */
  baseURL: string
  model: string
  apiKeySet: boolean
  minRole: 'readonly' | 'editor' | 'admin'
  dailyLimitPerUser: number
}

export interface AISettingsPayload extends Omit<AISettings, 'apiKeySet'> {
  /** Empty = keep the current one. */
  apiKey?: string
  clearAPIKey?: boolean
}

export function updateAI(payload: AISettingsPayload) {
  return api.put<AISettings>('/settings/ai', payload).then((r) => r.data)
}

export interface AITestResult {
  ok: boolean
  latencyMs?: number
  reply?: string
  error?: string
}

/** Sends a one-line prompt with the SAVED configuration. A provider failure comes back as 422 with its message. */
export function testAI() {
  return api
    .post<AITestResult>('/settings/ai/test', {}, { validateStatus: (s) => s === 200 || s === 422 })
    .then((r) => r.data)
}
