import { lazy, Suspense, type ComponentType, type ReactNode } from 'react'
import { Routes, Route, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Layout } from './components/Layout'
import { ProtectedRoute } from './components/ProtectedRoute'
import { AdminRoute } from './components/AdminRoute'
import { Spinner } from './components/ui'
import { listClusters } from './api/clusters'
import { useDocumentTitle } from './lib/brand'
import { Login } from './pages/Login'
import { Setup } from './pages/Setup'
import { NotFound } from './pages/NotFound'
import { isOptionalKind } from './api/resources'

// Each page in its own chunk: the bundle was a single 1.6 MB one with the 28
// pages, CodeMirror and xterm inside, and a readonly user who only looks at
// pods downloaded the whole thing. Login and Setup are static because they are
// the first thing painted. `isOptionalKind` comes from api/resources and not
// from the page: importing the page statically dragged CodeMirror into the
// main chunk.
function page<K extends string>(loader: () => Promise<Record<K, ComponentType<never>>>, name: K) {
  return lazy(() => loader().then((m) => ({ default: m[name] as ComponentType })))
}

const ChangePassword = page(() => import('./pages/ChangePassword'), 'ChangePassword')
const Profile = page(() => import('./pages/Profile'), 'Profile')
const Dashboard = page(() => import('./pages/Dashboard'), 'Dashboard')
const FleetAttention = page(() => import('./pages/FleetAttention'), 'FleetAttention')
const ClusterOverview = page(() => import('./pages/ClusterOverview'), 'ClusterOverview')
const ClusterView = page(() => import('./pages/ClusterView'), 'ClusterView')
const ClusterResources = lazy(() => import('./pages/ClusterResources').then((m) => ({ default: m.ClusterResources })))
const ResourceDetail = page(() => import('./pages/ResourceDetail'), 'ResourceDetail')
const HelmReleases = page(() => import('./pages/HelmReleases'), 'HelmReleases')
const HelmCatalog = page(() => import('./pages/HelmCatalog'), 'HelmCatalog')
const HelmRepos = page(() => import('./pages/HelmRepos'), 'HelmRepos')
const GitOpsSources = page(() => import('./pages/GitOpsSources'), 'GitOpsSources')
const DockerContainers = page(() => import('./pages/DockerPages'), 'DockerContainers')
const DockerImages = page(() => import('./pages/DockerPages'), 'DockerImages')
const DockerNetworks = page(() => import('./pages/DockerPages'), 'DockerNetworks')
const DockerOverview = page(() => import('./pages/DockerPages'), 'DockerOverview')
const DockerVolumes = page(() => import('./pages/DockerPages'), 'DockerVolumes')
const ClusterAdmin = page(() => import('./pages/ClusterAdmin'), 'ClusterAdmin')
const UserAdmin = page(() => import('./pages/UserAdmin'), 'UserAdmin')
const AuditLog = page(() => import('./pages/AuditLog'), 'AuditLog')
const GroupMappings = page(() => import('./pages/GroupMappings'), 'GroupMappings')
const Settings = page(() => import('./pages/Settings'), 'Settings')
const CloudCredentials = page(() => import('./pages/CloudCredentials'), 'CloudCredentials')
const Notifications = page(() => import('./pages/Notifications'), 'Notifications')
const ImageRegistries = page(() => import('./pages/ImageRegistries'), 'ImageRegistries')
const ClusterRegistries = page(() => import('./pages/ClusterRegistries'), 'ClusterRegistries')
const KubectlShell = page(() => import('./pages/KubectlShell'), 'KubectlShell')
const ApiDocs = page(() => import('./pages/ApiDocs'), 'ApiDocs')
const BackupTargets = page(() => import('./pages/BackupTargets'), 'BackupTargets')
const ClusterBackups = page(() => import('./pages/ClusterBackups'), 'ClusterBackups')
const ClusterImages = page(() => import('./pages/ClusterImages'), 'ClusterImages')
const ClusterExtension = page(() => import('./pages/ClusterExtension'), 'ClusterExtension')
const ClusterCosts = page(() => import('./pages/ClusterCosts'), 'ClusterCosts')
const ClusterTopology = page(() => import('./pages/ClusterTopology'), 'ClusterTopology')
const UiExtensions = page(() => import('./pages/UiExtensions'), 'UiExtensions')

// A Docker environment (kind=docker) has its own views; everything else is
// Kubernetes. Decided from the cluster list already cached by the Layout.
// While the list has not arrived, `loading` avoids mounting ClusterView, which
// redirects the tabs it does not know (containers, images…) to pods.
function useClusterKind(id: string | undefined) {
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const kind = clustersQuery.data?.find((c) => c.id === Number(id))?.kind
  return { kind, loading: clustersQuery.isPending }
}

function ClusterHome() {
  const { id } = useParams<{ id: string }>()
  const { kind, loading } = useClusterKind(id)
  if (loading) return null
  if (kind === 'docker') return <DockerOverview />
  return <ClusterOverview />
}

// The main kinds (pods, deployments…) have their own table in ClusterView;
// the secondary ones share the generic view. Decided here, outside both, so
// hooks are not made conditional inside a component.
function ClusterTab() {
  const { tab, id } = useParams<{ tab: string; id: string }>()
  const { kind, loading } = useClusterKind(id)
  if (loading) return null
  if (kind === 'docker') {
    if (tab === 'containers') return <DockerContainers />
    if (tab === 'images') return <DockerImages />
    if (tab === 'volumes') return <DockerVolumes />
    if (tab === 'networks') return <DockerNetworks />
    return <DockerOverview />
  }
  if (tab === 'helm') return <HelmReleases />
  if (tab === 'catalog') return <HelmCatalog />
  if (tab === 'gitops') return <GitOpsSources />
  if (tab === 'registries') return <ClusterRegistries />
  if (tab === 'shell') return <KubectlShell />
  if (tab === 'backups') return <ClusterBackups />
  if (tab === 'images') return <ClusterImages />
  if (tab === 'costs') return <ClusterCosts />
  if (tab === 'topology') return <ClusterTopology />
  if (isOptionalKind(tab)) return <ClusterResources key={tab} kind={tab} />
  return <ClusterView />
}

function admin(element: ReactNode) {
  return <AdminRoute>{element}</AdminRoute>
}

function App() {
  useDocumentTitle()
  return (
    <Suspense fallback={<Spinner className="p-6" />}>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/setup" element={<Setup />} />
        <Route
          element={
            <ProtectedRoute>
              <Layout />
            </ProtectedRoute>
          }
        >
          <Route path="/" element={<Dashboard />} />
          <Route path="/fleet/attention" element={<FleetAttention />} />
          <Route path="/change-password" element={<ChangePassword />} />
          <Route path="/profile" element={<Profile />} />
          <Route path="/api-docs" element={<ApiDocs />} />
          {/* The route without a tab is the cluster overview; the resource kind is
              one more segment, not local state, so one can link to "the
              deployments of this cluster". */}
          <Route path="/clusters/:id" element={<ClusterHome />} />
          <Route path="/clusters/:id/:tab" element={<ClusterTab />} />
          <Route path="/clusters/:id/ext/:extId" element={<ClusterExtension />} />
          {/* Resource detail: one URL per object (namespace "-" for cluster-scoped
              kinds) and the tab as an optional segment. */}
          <Route path="/clusters/:id/:tab/:namespace/:name/:view?" element={<ResourceDetail />} />
          <Route path="/admin/clusters" element={admin(<ClusterAdmin />)} />
          <Route path="/admin/users" element={admin(<UserAdmin />)} />
          <Route path="/admin/group-mappings" element={admin(<GroupMappings />)} />
          <Route path="/admin/backup-targets" element={admin(<BackupTargets />)} />
          <Route path="/admin/registries" element={admin(<ImageRegistries />)} />
          <Route path="/admin/notifications" element={admin(<Notifications />)} />
          <Route path="/admin/cloud-credentials" element={admin(<CloudCredentials />)} />
          <Route path="/admin/helm-repos" element={admin(<HelmRepos />)} />
          <Route path="/admin/extensions" element={admin(<UiExtensions />)} />
          <Route path="/admin/settings" element={admin(<Settings />)} />
          <Route path="/admin/audit" element={admin(<AuditLog />)} />
          {/* Inside the protected layout on purpose: an unknown URL from a user
              with a session must keep showing the navigation, and one from a
              user without a session is resolved first by ProtectedRoute sending
              them to /login. */}
          <Route path="*" element={<NotFound />} />
        </Route>
      </Routes>
    </Suspense>
  )
}

export default App
