import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
// Inter is loaded locally (no CDN, for air-gapped deployments) with the
// @font-face rules in index.css, which only bring the latin/latin-ext subsets.
import './index.css'
import './i18n'
import { initAppearance } from './lib/theme'
import { AuthProvider } from './hooks/useAuth'
import { PreferencesProvider } from './hooks/usePreferences'
import App from './App.tsx'
import { ConfirmProvider, TooltipProvider } from './components/ui'

// Apply the local theme and density cache BEFORE the first render, so the page
// does not flicker while /api/auth/me fetches the server preferences, which
// are the source of truth.
initAppearance()

// No retries on a 401: the response is not going to change (session revoked or
// user disabled) and every retry fires the /login redirect interceptor in
// lib/api.ts again.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error) =>
        (error as { response?: { status?: number } })?.response?.status === 401 ? false : failureCount < 3,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          {/* Inside AuthProvider: preferences arrive with the user. */}
          <PreferencesProvider>
            <TooltipProvider>
              <ConfirmProvider>
                <App />
              </ConfirmProvider>
            </TooltipProvider>
          </PreferencesProvider>
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
