import { api } from '../lib/api'
import type { CurrentUser } from '../hooks/useAuth'

export interface AuthStatus {
  setupPending: boolean
  ssoEnabled: boolean
  /** Name of the SSO button, configured in Settings (e.g. "Microsoft Entra ID"). */
  ssoDisplayName?: string
}

export function getAuthStatus() {
  return api.get<AuthStatus>('/auth/status').then((r) => r.data)
}

// Full-page navigation (not an axios call): the OIDC Authorization Code flow
// requires a top-level browser redirect to the identity provider, not an XHR
// request.
export function goToSsoLogin() {
  window.location.href = '/api/auth/oidc/login'
}

export function login(username: string, password: string) {
  return api.post<CurrentUser>('/auth/login', { username, password }).then((r) => r.data)
}

export function logout() {
  return api.post('/auth/logout')
}

export function me() {
  return api.get<CurrentUser>('/auth/me').then((r) => r.data)
}

export function completeSetup(token: string, newPassword: string) {
  return api.post<CurrentUser>('/auth/setup', { token, newPassword }).then((r) => r.data)
}

export function changePassword(currentPassword: string, newPassword: string) {
  return api
    .post<CurrentUser>('/auth/change-password', { currentPassword, newPassword })
    .then((r) => r.data)
}

/**
 * Profile data. Only for local users: the backend returns 409 for Entra ID
 * ones, whose name and email are sent by the IdP on every login.
 */
export function updateProfile(payload: { displayName?: string; email?: string }) {
  return api.patch<CurrentUser>('/auth/profile', payload).then((r) => r.data)
}

/** PARTIAL merge: only what changes is sent. */
export function updatePreferences(patch: Record<string, unknown>) {
  return api.patch<Record<string, unknown>>('/auth/preferences', patch).then((r) => r.data)
}
