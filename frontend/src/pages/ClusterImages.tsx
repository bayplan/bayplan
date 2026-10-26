import { useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink, FileText, ScanSearch, X } from 'lucide-react'
import * as imagesApi from '../api/images'
import { listClusters } from '../api/clusters'
import { SearchInput } from '../components/SearchInput'
import { Age, Alert, Badge, Button, Checkbox, DataTable, MenuItem, Modal, PageHeader, RowActions, Select, Toolbar, type BadgeTone, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { NotFound } from './NotFound'
import { apiErrorText, unavailableReason } from '../lib/errors'
import { safeHttpUrl } from '../lib/url'

// A scan takes minutes and the inventory changes little: one minute is enough.
const POLL_INTERVAL_MS = 60000
const ICON = 'h-4 w-4 shrink-0'

/** The worst wins: critical red, high red, medium amber, only lows/none green. */
function scanTone(c: imagesApi.ScanCounts): BadgeTone {
  if (c.critical > 0 || c.high > 0) return 'danger'
  if (c.medium > 0) return 'warning'
  return 'success'
}

function SeverityBadges({ counts }: { counts: imagesApi.ScanCounts }) {
  return (
    <span className="inline-flex gap-1 font-mono text-xs">
      <Badge tone={counts.critical > 0 ? 'danger' : 'neutral'}>C {counts.critical}</Badge>
      <Badge tone={counts.high > 0 ? 'danger' : 'neutral'}>H {counts.high}</Badge>
      <Badge tone={counts.medium > 0 ? 'warning' : 'neutral'}>M {counts.medium}</Badge>
      <Badge tone="neutral">L {counts.low}</Badge>
    </span>
  )
}

/**
 * Inventory of the cluster's images (from the informer cache) with the state of their
 * vulnerability scan (Trivy on the server). A scan is global per
 * image: what was scanned for one cluster is valid for the others.
 */
export function ClusterImages() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const canWrite = cluster?.role === 'admin' || cluster?.role === 'editor'
  const [filters, setFilters] = useUrlState(['ns', 'q'] as const)
  const namespaces = splitNamespaces(filters.ns)
  const query = filters.q.trim().toLowerCase()

  const statusQuery = useQuery({ queryKey: ['scan-status'], queryFn: imagesApi.getScanStatus, refetchInterval: POLL_INTERVAL_MS })
  const imagesQuery = useQuery({ queryKey: ['images', clusterId, filters.ns], queryFn: () => imagesApi.listImages(clusterId, namespaces), enabled: validCluster, refetchInterval: POLL_INTERVAL_MS, retry: false })
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['images', clusterId] })
    void queryClient.invalidateQueries({ queryKey: ['scan-status'] })
  }
  const [tableState, setTableState] = useTableState({ sortKey: 'severity', sortDir: 'desc' })
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [reportImage, setReportImage] = useState<string | null>(null)

  const scanMutation = useMutation({
    mutationFn: (body: { image?: string; all?: boolean; force?: boolean }) => imagesApi.enqueueScan(clusterId, body, namespaces),
    onSuccess: (res) => {
      invalidate()
      setError(null)
      setNotice(t('images.queued', { queued: res.queued, requested: res.requested }))
    },
    onError: (err) => setError(apiErrorText(err, t('images.error'))),
  })

  const filtered = useMemo(() => {
    const items = imagesQuery.data ?? []
    if (!query) return items
    return items.filter((i) => i.image.toLowerCase().includes(query) || i.workloads.some((w) => w.toLowerCase().includes(query)))
  }, [imagesQuery.data, query])

  if (!validCluster || (clustersQuery.data && !cluster)) return <NotFound />
  const available = statusQuery.data?.available ?? true

  const columns: DataTableColumn<imagesApi.ImageUsage>[] = [
    {
      key: 'image',
      header: t('images.image'),
      primary: true,
      render: (i) => (
        <div className="min-w-0">
          <div className="font-mono text-xs break-all">{i.image}</div>
          {i.digest && <div className="truncate font-mono text-[10px] text-fg-muted">{i.digest.slice(0, 19)}…</div>}
        </div>
      ),
      sortValue: (i) => i.image,
    },
    {
      key: 'usage',
      header: t('images.usedBy'),
      cellClassName: 'text-xs',
      render: (i) => (
        <span title={i.workloads.join('\n')}>
          {i.workloads.length === 1 ? i.workloads[0] : t('images.workloadsCount', { count: i.workloads.length })} · {t('images.podsCount', { count: i.pods })}
        </span>
      ),
      sortValue: (i) => i.pods,
    },
    {
      key: 'severity',
      header: t('images.vulnerabilities'),
      primary: true,
      render: (i) => {
        const s = i.scan
        if (!s) return <span className="text-xs text-fg-muted">{t('images.notScanned')}</span>
        if (s.status === 'queued' || s.status === 'running') return <Badge tone="warning">{t(`images.status.${s.status}`)}</Badge>
        if (s.status === 'error') {
          return (
            <span className="text-xs break-words text-danger" title={s.error}>
              {t('images.status.error')}: {s.error?.slice(0, 120)}
            </span>
          )
        }
        return (
          <div className="flex flex-wrap items-center gap-1">
            <SeverityBadges counts={s.counts} />
            {s.counts.fixable > 0 && <span className="text-xs text-fg-muted">{t('images.fixable', { count: s.counts.fixable })}</span>}
          </div>
        )
      },
      sortValue: (i) => {
        const c = i.scan?.counts
        if (!c || i.scan?.status !== 'ok') return -1
        return c.critical * 1_000_000 + c.high * 1_000 + c.medium
      },
    },
    {
      key: 'scanned',
      header: t('images.scannedAt'),
      primary: true,
      cellClassName: 'text-xs whitespace-nowrap text-fg-muted',
      render: (i) => (i.scan?.scannedAt ? <Age createdAt={i.scan.scannedAt} /> : '—'),
      sortValue: (i) => (i.scan?.scannedAt ? -new Date(i.scan.scannedAt).getTime() : 0),
    },
  ]

  const rowActions = (i: imagesApi.ImageUsage) => {
    const canReport = i.scan?.status === 'ok'
    const canScan = canWrite && available
    if (!canReport && !canScan) return null
    return (
      <RowActions>
        {canReport && (
          <MenuItem onSelect={() => setReportImage(i.image)}>
            <FileText className={ICON} aria-hidden="true" />
            {t('images.report')}
          </MenuItem>
        )}
        {canScan && (
          <MenuItem onSelect={() => scanMutation.mutate({ image: i.image, force: !!i.scan })} disabled={scanMutation.isPending || i.scan?.status === 'running' || i.scan?.status === 'queued'}>
            <ScanSearch className={ICON} aria-hidden="true" />
            {i.scan ? t('images.rescan') : t('images.scan')}
          </MenuItem>
        )}
      </RowActions>
    )
  }

  const unavailable = imagesQuery.isError ? unavailableReason(imagesQuery.error, t('common.error')) : undefined

  return (
    <div>
      <PageHeader
        title={t('images.title')}
        description={available ? t('images.hint', { version: statusQuery.data?.version ?? '' }) : t('images.unavailable')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('images.title') }]}
        actions={
          canWrite && available ? (
            <Button variant="primary" size="sm" onClick={() => scanMutation.mutate({ all: true })} disabled={scanMutation.isPending}>
              <ScanSearch className="h-3.5 w-3.5" aria-hidden="true" />
              {t('images.scanAll')}
              {statusQuery.data && statusQuery.data.queued > 0 ? ` (${t('images.inQueue', { count: statusQuery.data.queued })})` : ''}
            </Button>
          ) : undefined
        }
      />
      {error && (
        <Alert tone="danger" className="mb-3">
          {error}
        </Alert>
      )}
      {notice && (
        <Alert tone="info" className="mb-3" onClose={() => setNotice(null)}>
          {notice}
        </Alert>
      )}
      <Toolbar>
        <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v })} placeholder={t('common.searchPlaceholder')} />
        {query !== '' && (
          <Button size="sm" variant="ghost" onClick={() => setFilters({ q: '' })}>
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>
      {unavailable ? (
        <Alert tone="warning">{unavailable}</Alert>
      ) : (
        <DataTable
          columns={columns}
          data={filtered}
          rowKey={(i) => i.image}
          filtered={query !== ''}
          isLoading={imagesQuery.isPending}
          error={imagesQuery.isError ? apiErrorText(imagesQuery.error, t('images.error')) : undefined}
          emptyMessage={t('images.empty')}
          actions={rowActions}
          state={tableState}
          onStateChange={setTableState}
        />
      )}
      {reportImage && <ReportModal image={reportImage} onClose={() => setReportImage(null)} />}
    </div>
  )
}

const SEVERITIES = ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'UNKNOWN'] as const
const SEV_TONE: Record<string, BadgeTone> = { CRITICAL: 'danger', HIGH: 'danger', MEDIUM: 'warning', LOW: 'neutral', UNKNOWN: 'neutral' }

function ReportModal({ image, onClose }: { image: string; onClose: () => void }) {
  const { t } = useTranslation()
  const reportQuery = useQuery({ queryKey: ['image-report', image], queryFn: () => imagesApi.getImageReport(image) })
  const [minSeverity, setMinSeverity] = useState<string>('MEDIUM')
  const [onlyFixable, setOnlyFixable] = useState(false)
  const vulns = useMemo(() => {
    const all = reportQuery.data?.report?.vulnerabilities ?? []
    const idx = SEVERITIES.indexOf(minSeverity as (typeof SEVERITIES)[number])
    return all.filter((v) => SEVERITIES.indexOf(v.severity as (typeof SEVERITIES)[number]) <= (idx < 0 ? 99 : idx)).filter((v) => !onlyFixable || v.fixed)
  }, [reportQuery.data, minSeverity, onlyFixable])
  const rec = reportQuery.data
  return (
    <Modal
      onClose={onClose}
      size="xl"
      title={<span className="font-mono text-sm">{image}</span>}
      description={rec?.report?.os ? `${rec.report.os} · ${t('images.scannedWith', { version: rec.scannerVersion ?? '' })}` : undefined}
    >
      <>
        {rec && (
          <div className="mb-3 flex flex-wrap items-center gap-3">
            <SeverityBadges counts={rec.counts} />
            <Badge tone={scanTone(rec.counts)}>{t('images.totalCount', { count: rec.report?.vulnerabilities.length ?? 0 })}</Badge>
            <label className="flex items-center gap-1 text-xs">
              {t('images.minSeverity')}
              <Select value={minSeverity} onChange={(e) => setMinSeverity(e.target.value)} className="h-8 w-auto text-xs">
                {SEVERITIES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </Select>
            </label>
            <Checkbox label={t('images.onlyFixable')} className="items-center text-xs" checked={onlyFixable} onChange={(e) => setOnlyFixable(e.target.checked)} />
          </div>
        )}
        {reportQuery.isPending && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
        {rec && vulns.length === 0 && <p className="text-sm text-fg-muted">{t('images.noneAtLevel')}</p>}
        {vulns.length > 0 && (
          <table className="w-full text-xs">
            <thead className="text-left text-fg-muted">
              <tr>
                <th className="py-1 pr-2">CVE</th>
                <th className="py-1 pr-2">{t('images.severity')}</th>
                <th className="py-1 pr-2">{t('images.package')}</th>
                <th className="py-1 pr-2">{t('images.installed')}</th>
                <th className="py-1 pr-2">{t('images.fixedIn')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {vulns.slice(0, 500).map((v) => (
                <tr key={`${v.id}/${v.package}/${v.target}`} title={v.title}>
                  <td className="py-1 pr-2 font-mono whitespace-nowrap">
                    {safeHttpUrl(v.url) ? (
                      <a href={safeHttpUrl(v.url)} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-link hover:underline">
                        {v.id}
                        <ExternalLink className="h-3 w-3" aria-hidden="true" />
                      </a>
                    ) : (
                      v.id
                    )}
                  </td>
                  <td className="py-1 pr-2">
                    <Badge tone={SEV_TONE[v.severity] ?? 'neutral'}>{v.severity}</Badge>
                  </td>
                  <td className="py-1 pr-2 font-mono break-all">{v.package}</td>
                  <td className="py-1 pr-2 font-mono break-all">{v.installed}</td>
                  <td className="py-1 pr-2 font-mono break-all">{v.fixed || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {vulns.length > 500 && (
          <Alert tone="warning" className="mt-2">
            {t('images.truncated', { shown: 500, total: vulns.length })}
          </Alert>
        )}
      </>
    </Modal>
  )
}
