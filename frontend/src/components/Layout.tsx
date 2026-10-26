import { lazy, Suspense, useEffect, useState, type CSSProperties, type ReactNode } from 'react'
import { Outlet, Link, NavLink, useMatch } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowLeft,
  Box,
  Boxes,
  Container,
  CalendarClock,
  ChevronDown,
  Clock,
  Database,
  FileText,
  Gauge,
  GitBranch,
  Globe,
  HardDrive,
  Image as ImageIcon,
  KeyRound,
  Layers,
  LayoutDashboard,
  ListChecks,
  Network,
  Package,
  Archive,
  PanelLeftClose,
  PanelLeftOpen,
  Puzzle,
  Coins,
  Waypoints,
  ExternalLink,
  Bell,
  ScanSearch,
  Cloud,
  Ruler,
  SquareTerminal,
  PackageSearch,
  Scaling,
  ScrollText,
  Search,
  Server,
  Settings as SettingsIcon,
  Shield,
  UserCog,
  Users,
  UsersRound,
  Menu,
  X,
  Sparkles,
  type LucideIcon,
} from 'lucide-react'
import { ThemeToggle } from './ThemeToggle'
import { LanguageToggle } from './LanguageToggle'
import { Brand } from './Brand'
import { ClusterPicker } from './nav/ClusterPicker'
import { NamespacePicker } from './nav/NamespacePicker'
import { UserMenu } from './nav/UserMenu'
import { CommandPalette } from './nav/CommandPalette'
import { Spinner, StatusDot } from './ui'
import * as clustersApi from '../api/clusters'
import * as extApi from '../api/extensions'
import { getAIStatus } from '../api/ai'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { useAuth } from '../hooks/useAuth'
import { usePreferences } from '../hooks/usePreferences'
import { clusterConnection, connectionLabelKey, connectionTone } from '../lib/status'
import { cn } from '../lib/cn'
import { safeHttpUrl } from '../lib/url'

// Its own chunk: most sessions never open it.
const AssistantPanel = lazy(() => import('./AssistantPanel').then((m) => ({ default: m.AssistantPanel })))

// Cluster tabs whose view does NOT filter by namespace: cluster-scoped
// resources, and the ones that operate on the whole cluster. In all the others
// the topbar picker has an effect, so it is rendered. What ignores it is listed
// (and not what uses it) so that a new tab shows the picker by default: a
// missing filter gets noticed; one that does nothing does not.
const NAMESPACE_FREE_TABS: ReadonlySet<string> = new Set([
  'nodes',
  'persistentvolumes',
  'storageclasses',
  'namespaces',
  'registries',
  'backups',
])

interface SidebarContext {
  collapsed: boolean
  /** Closes the mobile drawer after navigating. */
  onNavigate: () => void
}

interface NavSectionProps {
  /** Stable key to persist whether it is folded. */
  id: string
  label: string
  sidebar: SidebarContext
  collapsedSections: string[]
  onToggle: (id: string) => void
  children: ReactNode
}

// Collapsible sidebar section. With the sidebar collapsed to icons the sections
// do not fold (there is no header to click) and are separated by a line.
function NavSection({ id, label, sidebar, collapsedSections, onToggle, children }: NavSectionProps) {
  const folded = collapsedSections.includes(id)
  if (sidebar.collapsed) {
    return <div className="space-y-0.5 border-t border-border pt-2 first:border-t-0 first:pt-0">{children}</div>
  }
  return (
    <div>
      {/* A label, not a control people look at: lighter than the items it
          groups, and the fold chevron only when it is useful (hover, folded). */}
      <button
        type="button"
        onClick={() => onToggle(id)}
        aria-expanded={!folded}
        className="group mb-1 flex w-full items-center justify-between rounded-md px-3 py-0.5 text-[11px] font-medium tracking-wider text-fg-subtle uppercase hover:text-fg-muted"
      >
        <span className="truncate">{label}</span>
        <ChevronDown
          className={cn('h-3 w-3 transition-[transform,opacity]', folded ? '-rotate-90 opacity-100' : 'opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100')}
          aria-hidden="true"
        />
      </button>
      {!folded && <div className="space-y-px">{children}</div>}
    </div>
  )
}

interface NavItemProps {
  to: string
  icon: LucideIcon
  label: string
  end?: boolean
  sidebar: SidebarContext
}

function NavItem({ to, icon: Icon, label, end, sidebar }: NavItemProps) {
  return (
    <NavLink
      to={to}
      end={end}
      title={sidebar.collapsed ? label : undefined}
      onClick={sidebar.onNavigate}
      className={({ isActive }) =>
        cn(
          // py-2.5 on touch and py-1.5 on desktop: 40 px minimum height with a
          // finger, dense with the mouse.
          'group relative flex items-center gap-2.5 rounded-md px-3 py-2.5 text-sm transition-colors md:py-1.5',
          sidebar.collapsed && 'justify-center px-0',
          isActive
            ? 'bg-surface font-medium text-fg shadow-sm ring-1 ring-border before:absolute before:inset-y-2 before:left-1 before:w-0.5 before:rounded-full before:bg-accent [&_svg]:text-fg'
            : 'text-fg/80 hover:bg-sidebar-hover hover:text-fg [&_svg]:text-fg-subtle hover:[&_svg]:text-fg-muted',
        )
      }
    >
      <Icon aria-hidden="true" />
      {!sidebar.collapsed && <span className="truncate">{label}</span>}
    </NavLink>
  )
}

// External sidebar link: same look as NavItem but never "active" (it opens
// another tab) and with rel noopener so the tool cannot navigate this
// window.
function ExternalNavItem({ href: raw, label, sidebar }: { href: string; label: string; sidebar: SidebarContext }) {
  // Re-checked after the placeholder substitution, like the embedded mode.
  const href = safeHttpUrl(raw)
  if (!href) return null
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      title={sidebar.collapsed ? label : undefined}
      className={cn(
        'flex items-center gap-2.5 rounded-md px-3 py-2.5 text-sm text-fg/80 transition-colors hover:bg-sidebar-hover hover:text-fg md:py-1.5 [&_svg]:text-fg-subtle',
        sidebar.collapsed && 'justify-center px-0',
      )}
    >
      <ExternalLink aria-hidden="true" />
      {!sidebar.collapsed && <span className="truncate">{label}</span>}
    </a>
  )
}

// Colors of the ThemeToggle/LanguageToggle BUTTON on the topbar. They are
// dedicated variables (--toggle-*, falling back to the normal tokens in
// Login/Setup) and not a reassignment of --color-surface/--color-fg: the
// language dropdown inherited that too, and rendered translucent with light
// text over the white content, and with no hover highlight in dark theme.
const TOPBAR_SCOPE_STYLE = {
  '--toggle-text': 'var(--color-topbar-text)',
  '--toggle-muted': 'var(--color-topbar-muted)',
  '--toggle-hover': 'var(--color-topbar-surface)',
} as CSSProperties

/**
 * The application's single shell: fixed topbar + fixed sidebar + `<main>` as
 * the ONLY scrolling element. Before, the whole document scrolled and, on
 * scrolling down, the cluster picker, the search box and the menu disappeared.
 *
 * The sidebar has two modes that never mix: global
 * (fleet + administration) outside a cluster, and the cluster's inside (header
 * with name and status, back link, grouped resources). To reach
 * Administration from a cluster you go back to the fleet: stacking the two
 * menus gave ~40 entries without collapsing.
 */
export function Layout() {
  const { t } = useTranslation()
  const { user } = useAuth()
  const { preferences, update } = usePreferences()
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)

  // Ctrl/Cmd+K. The preventDefault is essential: in Chrome and Firefox that
  // shortcut focuses the address bar and without it the palette would open
  // with the focus outside the page.
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen(true)
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  // The active cluster is taken from the route and not from global state: the
  // URL is already the source of truth for what is being viewed. Layout is the
  // element of a route without a path, so useParams does not see the child
  // routes' params and the pattern has to be matched by hand.
  const tabMatch = useMatch('/clusters/:id/:tab')
  const baseMatch = useMatch('/clusters/:id')
  const extMatch = useMatch('/clusters/:id/ext/:extId')
  const detailMatch = useMatch('/clusters/:id/:tab/:namespace/:name/*')
  const rawId = tabMatch?.params.id ?? baseMatch?.params.id ?? extMatch?.params.id ?? detailMatch?.params.id
  const parsedId = Number(rawId)
  const activeClusterId = Number.isInteger(parsedId) && parsedId > 0 ? parsedId : undefined
  const activeTab = tabMatch?.params.tab ?? detailMatch?.params.tab

  // Same queryKey as the rest of the app: React Query shares the cache and this
  // adds no request.
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: clustersApi.listClusters })
  const activeCluster = clustersQuery.data?.find((c) => c.id === activeClusterId)
  const inCluster = activeClusterId !== undefined
  const clusterKindKnown = activeCluster !== undefined

  const clusterHref = (tab: string) => `/clusters/${activeClusterId}/${tab}`

  // External tools of the active cluster. The "link" mode ones carry the
  // filter's namespace in the URL if the template asks for it; the embedded
  // ones are resolved by their page.
  const [filters] = useUrlState(['ns'] as const)
  const extQuery = useQuery({
    queryKey: ['cluster-extensions', activeClusterId],
    queryFn: () => extApi.listClusterExtensions(activeClusterId as number),
    enabled: activeClusterId !== undefined && activeCluster?.kind === 'kubernetes',
  })
  const firstNamespace = splitNamespaces(filters.ns ?? '')[0] ?? ''
  const aiStatus = useQuery({ queryKey: ['ai-status'], queryFn: getAIStatus, staleTime: 60_000, retry: false })
  // Closed = forgotten: the conversation lives in the panel's state only.
  const [assistantOpen, setAssistantOpen] = useState(false)

  const collapsed = preferences.sidebarCollapsed
  const sidebar: SidebarContext = { collapsed, onNavigate: () => setMobileNavOpen(false) }
  const collapsedSections = preferences.navCollapsed
  function toggleSection(id: string) {
    void update({
      navCollapsed: collapsedSections.includes(id) ? collapsedSections.filter((s) => s !== id) : [...collapsedSections, id],
    })
  }
  const sectionProps = { sidebar, collapsedSections, onToggle: toggleSection }

  const showNamespacePicker =
    activeCluster !== undefined &&
    activeCluster.kind !== 'docker' &&
    ((activeTab !== undefined && !NAMESPACE_FREE_TABS.has(activeTab)) || extMatch !== null)

  const connection = activeCluster ? clusterConnection(activeCluster) : undefined

  return (
    <div className="flex h-dvh flex-col overflow-hidden">
      {paletteOpen && (
        <CommandPalette
          onClose={() => setPaletteOpen(false)}
          activeClusterId={activeClusterId}
          isGlobalAdmin={user?.role === 'admin'}
        />
      )}
      {/* pt with safe-area: on a notched iPhone, in app mode, the bar extends
          under the notch and the content moves down just enough. */}
      <header className="z-(--z-topbar) flex h-[calc(3.5rem+env(safe-area-inset-top))] shrink-0 items-center justify-between gap-2 border-b border-topbar-border bg-topbar px-3 pt-[env(safe-area-inset-top)] text-topbar-text md:gap-3 md:px-4">
        <div className="flex min-w-0 items-center gap-2 md:gap-3">
          <button
            type="button"
            onClick={() => setMobileNavOpen((open) => !open)}
            className="rounded-md p-2 text-topbar-muted hover:bg-topbar-surface hover:text-topbar-text md:hidden"
            aria-label={t('nav.toggleMenu')}
            aria-expanded={mobileNavOpen}
          >
            {mobileNavOpen ? <X className="h-5 w-5" aria-hidden="true" /> : <Menu className="h-5 w-5" aria-hidden="true" />}
          </button>
          <button
            type="button"
            onClick={() => void update({ sidebarCollapsed: !collapsed })}
            className="hidden rounded-md p-1.5 text-topbar-muted hover:bg-topbar-surface hover:text-topbar-text md:block"
            aria-label={collapsed ? t('nav.expandSidebar') : t('nav.collapseSidebar')}
            title={collapsed ? t('nav.expandSidebar') : t('nav.collapseSidebar')}
          >
            {collapsed ? <PanelLeftOpen className="h-5 w-5" aria-hidden="true" /> : <PanelLeftClose className="h-5 w-5" aria-hidden="true" />}
          </button>
          <Link to="/" className="hidden text-topbar-text sm:block">
            <Brand size="sm" tone="inherit" />
          </Link>
          <div className="flex min-w-0 items-center gap-1.5 md:gap-2">
            <ClusterPicker activeClusterId={activeClusterId} activeTab={activeTab} />
            {showNamespacePicker && activeCluster && <NamespacePicker clusterId={activeCluster.id} />}
          </div>
        </div>

        <div className="flex shrink-0 items-center gap-1 md:gap-2">
          {/* A button besides the shortcut: a shortcut that is not visible does
              not exist for whoever does not know it. On mobile it does not fit
              and the palette is keyboard-driven: out. */}
          {aiStatus.data?.ready && (
            <button
              type="button"
              onClick={() => setAssistantOpen((open) => !open)}
              title={t('assistant.title')}
              aria-label={t('assistant.title')}
              aria-pressed={assistantOpen}
              className={cn('flex items-center gap-1.5 rounded-md px-2 py-1.5 text-topbar-muted hover:bg-topbar-surface hover:text-topbar-text', assistantOpen && 'bg-topbar-surface text-topbar-text')}
            >
              <Sparkles aria-hidden="true" />
              <span className="hidden text-sm lg:inline">{t('assistant.short')}</span>
            </button>
          )}
          <button
            type="button"
            onClick={() => setPaletteOpen(true)}
            title={t('palette.open')}
            aria-label={t('palette.open')}
            className="hidden items-center gap-1.5 rounded-md px-2 py-1.5 text-topbar-muted hover:bg-topbar-surface hover:text-topbar-text md:flex"
          >
            <Search aria-hidden="true" />
            <kbd className="hidden rounded border border-topbar-border px-1 text-[10px] lg:inline">Ctrl K</kbd>
          </button>
          {/* Language and theme in the topbar only when there is room; on mobile
              they go at the foot of the navigation drawer. */}
          <div className="hidden items-center gap-3 md:flex" style={TOPBAR_SCOPE_STYLE}>
            <LanguageToggle />
            <ThemeToggle />
          </div>
          <div className="border-l border-topbar-border pl-1 md:pl-2">
            <UserMenu />
          </div>
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        {mobileNavOpen && (
          <div
            className="fixed inset-0 top-[calc(3.5rem+env(safe-area-inset-top))] z-(--z-drawer-nav) bg-black/40 md:hidden"
            onClick={() => setMobileNavOpen(false)}
            aria-hidden="true"
          />
        )}
        <aside
          className={cn(
            // Mobile: drawer under the topbar (which stays visible with the
            // close button). Desktop: fixed column that never page-scrolls.
            'fixed inset-y-0 left-0 top-[calc(3.5rem+env(safe-area-inset-top))] z-(--z-drawer-nav) flex w-72 max-w-[85vw] -translate-x-full flex-col border-r border-border bg-sidebar transition-[transform,width] md:static md:top-auto md:z-auto md:max-w-none md:translate-x-0',
            collapsed ? 'md:w-14' : 'md:w-60',
            mobileNavOpen && 'translate-x-0',
          )}
        >
          {inCluster && (
            <div className={cn('border-b border-border', collapsed ? 'px-2 py-2' : 'px-3 py-3')}>
              <Link
                to="/"
                onClick={sidebar.onNavigate}
                title={t('nav.allClusters')}
                className={cn(
                  'flex items-center gap-1.5 text-xs text-fg-muted hover:text-fg',
                  collapsed && 'justify-center',
                )}
              >
                <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
                {!collapsed && t('nav.allClusters')}
              </Link>
              {!collapsed && (
                <div className="mt-1.5 flex items-center gap-2">
                  {connection && <StatusDot tone={connectionTone(connection)} title={t(connectionLabelKey(connection))} />}
                  <span className="truncate text-sm font-semibold text-fg" title={activeCluster?.displayName}>
                    {activeCluster?.displayName ?? t('cluster.title')}
                  </span>
                </div>
              )}
            </div>
          )}
          <nav className="scrollbar-thin flex-1 space-y-5 overflow-y-auto px-2 py-4">
            {!inCluster && (
              <>
                <NavSection id="fleet" label={t('nav.sectionFleet')} {...sectionProps}>
                  <NavItem to="/" icon={LayoutDashboard} label={t('nav.dashboard')} end sidebar={sidebar} />
                  <NavItem to="/fleet/attention" icon={AlertTriangle} label={t('fleet.title')} sidebar={sidebar} />
                </NavSection>
                {user?.role === 'admin' && (
                  <>
                    <NavSection id="admin" label={t('nav.sectionAdmin')} {...sectionProps}>
                      <NavItem to="/admin/clusters" icon={Server} label={t('nav.clusters')} sidebar={sidebar} />
                      <NavItem to="/admin/users" icon={Users} label={t('nav.users')} sidebar={sidebar} />
                      <NavItem to="/admin/group-mappings" icon={UsersRound} label={t('groupMappings.navTitle')} sidebar={sidebar} />
                    </NavSection>
                    <NavSection id="integrations" label={t('nav.sectionIntegrations')} {...sectionProps}>
                      <NavItem to="/admin/helm-repos" icon={PackageSearch} label={t('helm.reposTitle')} sidebar={sidebar} />
                      <NavItem to="/admin/cloud-credentials" icon={Cloud} label={t('cloud.title')} sidebar={sidebar} />
                      <NavItem to="/admin/registries" icon={Container} label={t('registries.title')} sidebar={sidebar} />
                      <NavItem to="/admin/backup-targets" icon={Archive} label={t('backups.targetsTitle')} sidebar={sidebar} />
                      <NavItem to="/admin/notifications" icon={Bell} label={t('notify.title')} sidebar={sidebar} />
                      <NavItem to="/admin/extensions" icon={Puzzle} label={t('extensions.navTitle')} sidebar={sidebar} />
                    </NavSection>
                    <NavSection id="system" label={t('nav.sectionSystem')} {...sectionProps}>
                      <NavItem to="/admin/settings" icon={SettingsIcon} label={t('nav.settings')} sidebar={sidebar} />
                      <NavItem to="/admin/audit" icon={ScrollText} label={t('audit.title')} sidebar={sidebar} />
                    </NavSection>
                  </>
                )}
              </>
            )}

            {/* While the cluster kind is unknown no menu is rendered: before,
                the Kubernetes one showed even when the environment was Docker. */}
            {inCluster && clusterKindKnown && activeCluster.kind === 'docker' && (
              <NavSection id="docker" label={t('nav.sectionEnvironment')} {...sectionProps}>
                <NavItem to={`/clusters/${activeClusterId}`} icon={Gauge} label={t('overview.title')} end sidebar={sidebar} />
                <NavItem to={clusterHref('containers')} icon={Container} label={t('docker.containers')} sidebar={sidebar} />
                <NavItem to={clusterHref('images')} icon={ImageIcon} label={t('docker.images')} sidebar={sidebar} />
                <NavItem to={clusterHref('volumes')} icon={HardDrive} label={t('docker.volumes')} sidebar={sidebar} />
                <NavItem to={clusterHref('networks')} icon={Network} label={t('docker.networks')} sidebar={sidebar} />
              </NavSection>
            )}

            {inCluster && clusterKindKnown && activeCluster.kind !== 'docker' && (
              <>
                <NavSection id="cluster" label={t('nav.sectionCluster')} {...sectionProps}>
                  <NavItem to={`/clusters/${activeClusterId}`} icon={Gauge} label={t('overview.title')} end sidebar={sidebar} />
                  <NavItem to={clusterHref('topology')} icon={Waypoints} label={t('topology.title')} sidebar={sidebar} />
                  <NavItem to={clusterHref('nodes')} icon={Server} label={t('cluster.tabs.nodes')} sidebar={sidebar} />
                  <NavItem to={clusterHref('events')} icon={CalendarClock} label={t('cluster.tabs.events')} sidebar={sidebar} />
                  <NavItem to={clusterHref('costs')} icon={Coins} label={t('cluster.tabs.costs')} sidebar={sidebar} />
                </NavSection>
                <NavSection id="apps" label={t('nav.sectionApps')} {...sectionProps}>
                  <NavItem to={clusterHref('helm')} icon={Package} label={t('helm.releasesTitle')} sidebar={sidebar} />
                  <NavItem to={clusterHref('catalog')} icon={PackageSearch} label={t('helm.catalogTitle')} sidebar={sidebar} />
                  <NavItem to={clusterHref('gitops')} icon={GitBranch} label={t('gitops.title')} sidebar={sidebar} />
                  <NavItem to={clusterHref('images')} icon={ScanSearch} label={t('images.title')} sidebar={sidebar} />
                </NavSection>
                <NavSection id="workloads" label={t('nav.sectionWorkloads')} {...sectionProps}>
                  <NavItem to={clusterHref('deployments')} icon={Boxes} label={t('cluster.tabs.deployments')} sidebar={sidebar} />
                  <NavItem to={clusterHref('statefulsets')} icon={Database} label={t('cluster.tabs.statefulsets')} sidebar={sidebar} />
                  <NavItem to={clusterHref('daemonsets')} icon={Layers} label={t('cluster.tabs.daemonsets')} sidebar={sidebar} />
                  <NavItem to={clusterHref('pods')} icon={Box} label={t('cluster.tabs.pods')} sidebar={sidebar} />
                  <NavItem to={clusterHref('jobs')} icon={ListChecks} label={t('cluster.tabs.jobs')} sidebar={sidebar} />
                  <NavItem to={clusterHref('cronjobs')} icon={Clock} label={t('cluster.tabs.cronjobs')} sidebar={sidebar} />
                  <NavItem to={clusterHref('horizontalpodautoscalers')} icon={Scaling} label={t('cluster.tabs.horizontalpodautoscalers')} sidebar={sidebar} />
                </NavSection>
                <NavSection id="network" label={t('nav.sectionNetwork')} {...sectionProps}>
                  <NavItem to={clusterHref('services')} icon={Network} label={t('cluster.tabs.services')} sidebar={sidebar} />
                  <NavItem to={clusterHref('ingresses')} icon={Globe} label={t('cluster.tabs.ingresses')} sidebar={sidebar} />
                  <NavItem to={clusterHref('networkpolicies')} icon={Shield} label={t('cluster.tabs.networkpolicies')} sidebar={sidebar} />
                </NavSection>
                <NavSection id="config" label={t('nav.sectionConfig')} {...sectionProps}>
                  <NavItem to={clusterHref('configmaps')} icon={FileText} label={t('cluster.tabs.configmaps')} sidebar={sidebar} />
                  <NavItem to={clusterHref('secrets')} icon={KeyRound} label={t('cluster.tabs.secrets')} sidebar={sidebar} />
                  <NavItem to={clusterHref('serviceaccounts')} icon={UserCog} label={t('cluster.tabs.serviceaccounts')} sidebar={sidebar} />
                  <NavItem to={clusterHref('resourcequotas')} icon={Gauge} label={t('cluster.tabs.resourcequotas')} sidebar={sidebar} />
                  <NavItem to={clusterHref('limitranges')} icon={Ruler} label={t('cluster.tabs.limitranges')} sidebar={sidebar} />
                </NavSection>
                <NavSection id="storage" label={t('nav.sectionStorage')} {...sectionProps}>
                  <NavItem to={clusterHref('persistentvolumeclaims')} icon={HardDrive} label={t('cluster.tabs.persistentvolumeclaims')} sidebar={sidebar} />
                  <NavItem to={clusterHref('persistentvolumes')} icon={HardDrive} label={t('cluster.tabs.persistentvolumes')} sidebar={sidebar} />
                  <NavItem to={clusterHref('storageclasses')} icon={Database} label={t('cluster.tabs.storageclasses')} sidebar={sidebar} />
                </NavSection>
                {(extQuery.data?.length ?? 0) > 0 && (
                  <NavSection id="tools" label={t('nav.sectionTools')} {...sectionProps}>
                    {extQuery.data?.map((ext) =>
                      ext.mode === 'embed' ? (
                        <NavItem key={ext.id} to={`/clusters/${activeClusterId}/ext/${ext.id}`} icon={Puzzle} label={ext.name} sidebar={sidebar} />
                      ) : (
                        <ExternalNavItem key={ext.id} href={extApi.expandExtensionUrl(ext.url, activeCluster, firstNamespace)} label={ext.name} sidebar={sidebar} />
                      ),
                    )}
                  </NavSection>
                )}
                {/* Operations last and by role: these are the actions that touch
                    the whole cluster, not a resource. */}
                {(activeCluster.role === 'admin' || activeCluster.role === 'editor') && (
                  <NavSection id="ops" label={t('nav.sectionOps')} {...sectionProps}>
                    {activeCluster.role === 'admin' && (
                      <NavItem to={clusterHref('shell')} icon={SquareTerminal} label={t('kubectl.title')} sidebar={sidebar} />
                    )}
                    {activeCluster.role === 'admin' && (
                      <NavItem to={clusterHref('backups')} icon={Archive} label={t('backups.title')} sidebar={sidebar} />
                    )}
                    <NavItem to={clusterHref('registries')} icon={Container} label={t('registries.clusterTitle')} sidebar={sidebar} />
                  </NavSection>
                )}
              </>
            )}
          </nav>
          {/* Foot of the mobile drawer: language and theme, which do not fit in the topbar. */}
          <div className="flex items-center justify-between gap-2 border-t border-border px-3 py-2 md:hidden">
            <LanguageToggle />
            <ThemeToggle />
            <button
              type="button"
              onClick={() => setMobileNavOpen(false)}
              className="ml-auto rounded-md p-2 text-fg-muted hover:bg-sidebar-hover hover:text-fg"
              aria-label={t('common.close')}
            >
              <X aria-hidden="true" />
            </button>
          </div>
        </aside>

        {/* The application's only scroll. Full-height views (shell, terminal,
            embedded extension) use `h-full` + `flex-1 min-h-0` and never
            `calc(100vh - …)`. */}
        <div className="flex min-w-0 flex-1 flex-col">
          <main className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 py-4 md:px-6 md:py-5">
            {/* Suspense here and not only in App: that way when changing page the
                shell stays and only the content shows the chunk's spinner. */}

            <Suspense fallback={<Spinner />}>
              <Outlet />
            </Suspense>
          </main>
          {/* Below <main>, not over it: the page stays usable while asking, and
              it survives navigation because the Layout does. */}
          {assistantOpen && (
            <Suspense fallback={null}>
              <AssistantPanel
                clusterId={activeCluster?.kind === 'kubernetes' ? activeCluster.id : undefined}
                namespaces={splitNamespaces(filters.ns ?? '')}
                onClose={() => setAssistantOpen(false)}
              />
            </Suspense>
          )}
        </div>
      </div>
    </div>
  )
}
