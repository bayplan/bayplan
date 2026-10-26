import axios from 'axios'

// HTTP client against the backend API. `withCredentials` sends the httpOnly
// session cookie on every request; the backend is what resolves the session,
// the frontend never sees the token. CSRF protection (double-submit) is
// completed in Phase 1 together with the auth endpoints: the backend exposes
// the CSRF token in a JS-readable cookie and this interceptor forwards it as
// a header on mutating requests.
export const api = axios.create({
  baseURL: '/api',
  withCredentials: true,
})

function getCsrfCookie(): string | undefined {
  return document.cookie
    .split('; ')
    .find((row) => row.startsWith('bayplan_csrf='))
    ?.split('=')[1]
}

api.interceptors.request.use((config) => {
  const method = config.method?.toUpperCase()
  if (method && !['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    const csrf = getCsrfCookie()
    if (csrf) config.headers['X-CSRF-Token'] = csrf
  }
  return config
})

// Routes whose 401 is a legitimate business response and does NOT mean "your
// session is no longer valid": wrong credentials on login, wrong current
// password when changing it, and the startup /me (which also runs without a
// session, e.g. on /setup). If those cases redirected, a typo in the current
// password would kick the user out and the forced password-change flow would
// loop.
const NO_REDIRECT_ON_401 = ['/auth/login', '/auth/me', '/auth/change-password']

// A 401 that shows up mid-session (session revoked, user disabled) has to send
// the user to the login screen. Session state lives in AuthContext (memory),
// unreachable from here, so a full reload is forced: replace() also avoids
// leaving the protected route in the history.
api.interceptors.response.use(undefined, (error) => {
  const status = error?.response?.status
  const url = error?.config?.url ?? ''
  const exento = NO_REDIRECT_ON_401.some((ruta) => url.startsWith(ruta))
  if (status === 401 && !exento && window.location.pathname !== '/login') {
    window.location.replace('/login')
  }
  // Always re-throw: the mutations' onError handlers show the message.
  return Promise.reject(error)
})
