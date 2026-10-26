import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import * as authApi from '../api/auth'

export interface CurrentUser {
  id: number
  username: string
  displayName?: string
  email?: string
  /** 'local' or 'oidc': SSO users cannot edit their profile here. */
  source: string
  role: 'admin' | 'editor' | 'readonly' | ''
  mustChangePassword: boolean
  /** Uninterpreted preferences document; usePreferences normalizes it. */
  preferences?: Record<string, unknown>
}

interface AuthContextValue {
  user: CurrentUser | null
  loading: boolean
  /** Returns the user so the caller can act according to their preferences. */
  login: (username: string, password: string) => Promise<CurrentUser>
  logout: () => Promise<void>
  setup: (token: string, newPassword: string) => Promise<void>
  changePassword: (currentPassword: string, newPassword: string) => Promise<void>
  updateProfile: (payload: { displayName?: string; email?: string }) => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<CurrentUser | null>(null)
  const [loading, setLoading] = useState(true)
  const queryClient = useQueryClient()
  // Whether a user was ever set in this tab: going from one to none (logout,
  // a /me that no longer resolves) has to drop the TanStack Query cache, or
  // the next person to log in on the same tab is served the previous one's
  // cached data (Secret YAML, users and roles…) until each query refetches.
  const hadUser = useRef(false)

  useEffect(() => {
    if (user) hadUser.current = true
    else if (hadUser.current) {
      hadUser.current = false
      queryClient.clear()
    }
  }, [user, queryClient])

  useEffect(() => {
    authApi
      .me()
      .then(setUser)
      .catch(() => setUser(null))
      .finally(() => setLoading(false))
  }, [])

  async function login(username: string, password: string) {
    const logged = await authApi.login(username, password)
    setUser(logged)
    return logged
  }

  async function logout() {
    // The local session is dropped even if the request fails (network, an
    // already-expired session): the user asked to leave, and leaving the UI
    // logged in with a dead cookie helps nobody.
    try {
      await authApi.logout()
    } catch {
      // ignored on purpose, see above
    } finally {
      setUser(null)
      queryClient.clear()
    }
  }

  async function setup(token: string, newPassword: string) {
    setUser(await authApi.completeSetup(token, newPassword))
  }

  async function changePassword(currentPassword: string, newPassword: string) {
    setUser(await authApi.changePassword(currentPassword, newPassword))
  }

  async function updateProfile(payload: { displayName?: string; email?: string }) {
    setUser(await authApi.updateProfile(payload))
  }

  return (
    <AuthContext.Provider value={{ user, loading, login, logout, setup, changePassword, updateProfile }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')

  return ctx
}
