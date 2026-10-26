import { useMemo } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import * as metricsApi from '../api/metrics'
import { listClusters } from '../api/clusters'
import { useAuth } from '../hooks/useAuth'
import { X } from 'lucide-react'
import { SearchInput } from '../components/SearchInput'
import { Alert, Button, Card, DataTable, PageHeader, Toolbar, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { formatBytes, formatCpu } from '../lib/format'
import { apiErrorText } from '../lib/errors'

/**
 * Estimated cost per namespace: allocation of the prices from Settings over what
 * is reserved (requests) and, with metrics-server, what is consumed. It is an allocation
 * estimate and the page says so; it does not replace the provider's invoice.
 */
export function ClusterCosts() {
  const { id } = useParams<{ id: string }>()
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage ?? 'en'
  const { user } = useAuth()
  const clusterId = Number(id)
  const [filters, setFilters] = useUrlState(['ns', 'q'] as const)
  const namespaces = splitNamespaces(filters.ns)
  const query = filters.q.trim().toLowerCase()
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const costsQuery = useQuery({ queryKey: ['costs', clusterId, filters.ns], queryFn: () => metricsApi.getCosts(clusterId, namespaces), enabled: Number.isInteger(clusterId), refetchInterval: 60_000, retry: false })
  const [tableState, setTableState] = useTableState({ sortKey: 'billable', sortDir: 'desc' })

  const rep = costsQuery.data
  const filtered = useMemo(() => {
    const items = rep?.namespaces ?? []
    return query ? items.filter((r) => r.namespace.toLowerCase().includes(query)) : items
  }, [rep, query])
  const money = (v: number) => (rep?.configured ? `${v.toLocaleString(locale, { maximumFractionDigits: 2, minimumFractionDigits: 2 })} ${rep.prices.currency}` : '—')

  const columns: DataTableColumn<metricsApi.NamespaceCost>[] = [
    { key: 'namespace', header: t('cluster.columns.namespace'), primary: true, cellClassName: 'font-mono', render: (r) => r.namespace, sortValue: (r) => r.namespace },
    { key: 'pods', header: t('costs.pods'), primary: true, render: (r) => r.pods, sortValue: (r) => r.pods },
    { key: 'cpuReq', header: t('costs.cpuReserved'), render: (r) => formatCpu(r.cpuRequestMilli, locale), sortValue: (r) => r.cpuRequestMilli },
    { key: 'memReq', header: t('costs.memReserved'), render: (r) => formatBytes(r.memoryRequestBytes, locale), sortValue: (r) => r.memoryRequestBytes },
    ...(rep?.available
      ? [
          { key: 'cpuUsed', header: t('costs.cpuUsed'), render: (r) => formatCpu(r.cpuUsedMilli, locale), sortValue: (r) => r.cpuUsedMilli },
          { key: 'memUsed', header: t('costs.memUsed'), render: (r) => formatBytes(r.memoryUsedBytes, locale), sortValue: (r) => r.memoryUsedBytes },
        ] satisfies DataTableColumn<metricsApi.NamespaceCost>[]
      : []),
    { key: 'reserved', header: t('costs.reservedMonthly'), cellClassName: 'text-right tabular-nums', render: (r) => money(r.reservedMonthly), sortValue: (r) => r.reservedMonthly },
    ...(rep?.available ? [{ key: 'used', header: t('costs.usedMonthly'), cellClassName: 'text-right tabular-nums', render: (r) => money(r.usedMonthly), sortValue: (r) => r.usedMonthly } satisfies DataTableColumn<metricsApi.NamespaceCost>] : []),
    { key: 'billable', header: t('costs.billableMonthly'), primary: true, cellClassName: 'text-right font-semibold tabular-nums', render: (r) => money(r.billableMonthly), sortValue: (r) => r.billableMonthly },
  ]

  return (
    <div>
      <PageHeader title={t('costs.title')} description={t('costs.hint')} breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('costs.title') }]} />
      {rep && !rep.configured && (
        <Alert tone="info" className="mb-4">
          {t('costs.notConfigured')}{' '}
          {user?.role === 'admin' && (
            <Link to="/admin/settings" className="text-primary underline">
              {t('nav.settings')}
            </Link>
          )}
        </Alert>
      )}
      {rep && !rep.available && (
        <Alert tone="info" className="mb-4">
          {t('costs.noMetrics', { reason: rep.reason ?? '' })}
        </Alert>
      )}
      {rep && (
        <div className="mb-4 grid gap-3 sm:grid-cols-3">
          <Card className="min-w-0 p-4">
            <p className="text-xs text-fg-muted uppercase">{t('costs.reservedMonthly')}</p>
            <p className="mt-1 text-xl font-semibold tabular-nums">{money(rep.totals.reservedMonthly)}</p>
            <p className="text-xs text-fg-muted">
              {formatCpu(rep.totals.cpuRequestMilli, locale)} · {formatBytes(rep.totals.memoryRequestBytes, locale)}
            </p>
          </Card>
          {rep.available && (
            <Card className="min-w-0 p-4">
              <p className="text-xs text-fg-muted uppercase">{t('costs.usedMonthly')}</p>
              <p className="mt-1 text-xl font-semibold tabular-nums">{money(rep.totals.usedMonthly)}</p>
              <p className="text-xs text-fg-muted">
                {formatCpu(rep.totals.cpuUsedMilli, locale)} · {formatBytes(rep.totals.memoryUsedBytes, locale)}
              </p>
            </Card>
          )}
          <Card className="min-w-0 p-4">
            <p className="text-xs text-fg-muted uppercase">{t('costs.billableMonthly')}</p>
            <p className="mt-1 text-xl font-semibold tabular-nums">{money(rep.totals.billableMonthly)}</p>
            <p className="text-xs text-fg-muted">{t('costs.billableHint')}</p>
          </Card>
        </div>
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
      <DataTable
        columns={columns}
        data={filtered}
        rowKey={(r) => r.namespace}
        filtered={query !== ''}
        isLoading={costsQuery.isPending}
        error={costsQuery.isError ? apiErrorText(costsQuery.error, t('costs.error')) : undefined}
        emptyMessage={t('common.noData')}
        state={tableState}
        onStateChange={setTableState}
      />
      {rep?.configured && (
        <p className="mt-3 text-xs text-fg-muted">
          {t('costs.prices', { cpu: rep.prices.cpuCoreHour, mem: rep.prices.memoryGiBHour, currency: rep.prices.currency, hours: rep.hoursPerMonth })}
        </p>
      )}
    </div>
  )
}
