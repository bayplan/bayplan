import { useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { ExternalLink } from 'lucide-react'
import * as extApi from '../api/extensions'
import { listClusters } from '../api/clusters'
import { Button, PageHeader } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { safeHttpUrl } from '../lib/url'

/**
 * External tool embedded in an iframe. If the tool sends
 * X-Frame-Options / CSP frame-ancestors the browser leaves the frame blank
 * without the panel being able to know, so the "open in another tab" button
 * is always visible and the text explains it.
 */
export function ClusterExtension() {
  const { t } = useTranslation()
  const { id, extId } = useParams<{ id: string; extId: string }>()
  const clusterId = Number(id)
  const [filters] = useUrlState(['ns'] as const)
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const extQuery = useQuery({ queryKey: ['cluster-extensions', clusterId], queryFn: () => extApi.listClusterExtensions(clusterId), enabled: Number.isInteger(clusterId) })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const ext = extQuery.data?.find((e) => e.id === Number(extId))

  if (extQuery.isPending || clustersQuery.isPending) return <p className="text-sm text-fg-muted">{t('common.loading')}</p>
  if (!ext || !cluster) return <p className="text-sm text-fg-muted">{t('extensions.notFound')}</p>

  // The backend only accepts http(s), but the check is repeated after the
  // substitution: this string goes into an iframe and window.open.
  const url = safeHttpUrl(extApi.expandExtensionUrl(ext.url, cluster, splitNamespaces(filters.ns ?? '')[0] ?? ''))
  if (!url) return <p className="text-sm text-fg-muted">{t('extensions.notFound')}</p>
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={ext.name}
        description={t('extensions.embedNote')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster.displayName, to: `/clusters/${cluster.id}` }, { label: ext.name }]}
        actions={
          <Button onClick={() => window.open(url, '_blank', 'noopener')}>
            <ExternalLink className="h-4 w-4" aria-hidden="true" />
            {t('extensions.openExternal')}
          </Button>
        }
      />
      <div className="min-h-0 flex-1 overflow-hidden rounded-md border border-border bg-surface">
        <iframe key={url} src={url} title={ext.name} className="h-full w-full" sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-downloads" referrerPolicy="no-referrer" />
      </div>
    </div>
  )
}
