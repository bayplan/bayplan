import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useInfiniteQuery } from '@tanstack/react-query'
import { Download, Eye } from 'lucide-react'
import { auditExportUrl, listAudit, type AuditEntry, type AuditFilters } from '../api/audit'
import { Badge, Button, DataTable, Input, MenuItem, Modal, PageHeader, RowActions, Select, Toolbar, type DataTableColumn } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'
import { isoFromDatetimeLocal } from '../lib/dates'

const FILTER_KEYS = ['actor', 'action', 'cluster', 'result', 'q', 'from', 'to'] as const

const RESULT_TONES: Record<string, 'success' | 'warning' | 'danger'> = {
  success: 'success',
  denied: 'danger',
  failure: 'warning',
}

export function AuditLog() {
  const { t, i18n } = useTranslation()
  const [filters, setFilters] = useUrlState(FILTER_KEYS)
  const [detailOf, setDetailOf] = useState<AuditEntry | null>(null)

  // What gets sent to the backend: dates go in RFC3339 even though in the UI's
  // URL they are kept in the input's format, so the link stays
  // readable and reusable in the form itself.
  const query: AuditFilters = {
    actor: filters.actor,
    action: filters.action,
    cluster: filters.cluster,
    result: filters.result,
    q: filters.q,
    from: isoFromDatetimeLocal(filters.from),
    to: isoFromDatetimeLocal(filters.to),
  }

  // Cursor pagination: audit_log is partitioned by month and the query uses
  // keyset, so the natural shape in the UI is to keep appending pages, not jumping
  // to page N (which would require an OFFSET).
  const auditQuery = useInfiniteQuery({
    queryKey: ['audit', query],
    queryFn: ({ pageParam }) => listAudit(query, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  })

  const entries = auditQuery.data?.pages.flatMap((p) => p.items) ?? []
  const hasFilters = Object.values(filters).some(Boolean)

  const columns: DataTableColumn<AuditEntry>[] = [
    {
      key: 'ts',
      header: t('audit.columns.ts'),
      cellClassName: 'text-fg-muted md:whitespace-nowrap',
      render: (e) => new Date(e.ts).toLocaleString(i18n.resolvedLanguage),
      primary: true,
    },
    {
      key: 'actor',
      header: t('audit.columns.actor'),
      cellClassName: 'font-mono',
      // Without an actor these are the database triggers (changes made by direct
      // SQL), which have no HTTP session behind them.
      render: (e) => e.actorUsername || <span className="text-fg-muted">—</span>,
    },
    { key: 'action', header: t('audit.columns.action'), cellClassName: 'max-w-0 w-full break-all font-mono text-xs', render: (e) => e.action, primary: true },
    {
      key: 'result',
      header: t('audit.columns.result'),
      render: (e) => <Badge tone={RESULT_TONES[e.result] ?? 'neutral'}>{t(`audit.result.${e.result}`, { defaultValue: e.result })}</Badge>,
      primary: true,
    },
    {
      key: 'cluster',
      header: t('audit.columns.cluster'),
      render: (e) => e.cluster || <span className="text-fg-muted">—</span>,
    },
    {
      key: 'ip',
      header: t('audit.columns.ip'),
      cellClassName: 'font-mono text-xs text-fg-muted',
      render: (e) => e.ip || '—',
    },
  ]

  return (
    <div>
      <PageHeader
        title={t('audit.title')}
        description={t('audit.subtitle')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('audit.title') }]}
        actions={
          // A navigation and not a link wrapping a button (a <button> inside
          // an <a> is invalid and takes two Tab stops): the backend answers with
          // Content-Disposition, so the browser downloads and stays on the page.
          <Button size="sm" variant="secondary" onClick={() => window.location.assign(auditExportUrl(query))}>
            <Download className="h-3.5 w-3.5" aria-hidden="true" />
            {t('audit.export')}
          </Button>
        }
      />

      {/* Inline filters: the inputs use automatic width (the primitive is
          w-full by default for forms) so that several fit per row. */}
      <Toolbar>
        <Input
          value={filters.actor}
          onChange={(e) => setFilters({ actor: e.target.value })}
          placeholder={t('audit.columns.actor')}
          className="w-auto"
        />
        <Input
          value={filters.action}
          onChange={(e) => setFilters({ action: e.target.value })}
          placeholder={t('audit.columns.action')}
          className="w-auto"
        />
        <Input
          value={filters.cluster}
          onChange={(e) => setFilters({ cluster: e.target.value })}
          placeholder={t('audit.columns.cluster')}
          className="w-auto"
        />
        <Select value={filters.result} onChange={(e) => setFilters({ result: e.target.value })} className="w-auto">
          <option value="">{t('audit.allResults')}</option>
          <option value="success">{t('audit.result.success')}</option>
          <option value="denied">{t('audit.result.denied')}</option>
          <option value="failure">{t('audit.result.failure')}</option>
        </Select>
        <Input
          value={filters.q}
          onChange={(e) => setFilters({ q: e.target.value })}
          placeholder={t('audit.detailSearch')}
          className="w-auto min-w-48"
        />
        <label className="flex items-center gap-1.5 text-xs text-fg-muted">
          {t('cluster.events.sinceLabel')}
          <Input
            type="datetime-local"
            value={filters.from}
            onChange={(e) => setFilters({ from: e.target.value })}
            className="w-auto"
          />
        </label>
        <label className="flex items-center gap-1.5 text-xs text-fg-muted">
          {t('cluster.events.untilLabel')}
          <Input
            type="datetime-local"
            value={filters.to}
            onChange={(e) => setFilters({ to: e.target.value })}
            className="w-auto"
          />
        </label>
        {hasFilters && (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setFilters({ actor: '', action: '', cluster: '', result: '', q: '', from: '', to: '' })}
          >
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>

      {/* Own cursor pagination ("load more" button), not useTableState:
          the table cannot jump to page N without an OFFSET that the backend
          avoids on purpose. */}
      <DataTable
        columns={columns}
        data={entries}
        rowKey={(e) => String(e.id)}
        isLoading={auditQuery.isPending}
        error={auditQuery.isError ? apiErrorText(auditQuery.error, t('common.error')) : undefined}
        filtered={hasFilters}
        emptyMessage={t('audit.empty')}
        actions={(e) =>
          e.detail ? (
            <RowActions>
              <MenuItem onSelect={() => setDetailOf(e)}>
                <Eye className="h-4 w-4" aria-hidden="true" />
                {t('audit.viewDetail')}
              </MenuItem>
            </RowActions>
          ) : null
        }
      />

      {auditQuery.hasNextPage && (
        <div className="mt-3 text-center">
          <Button
            variant="secondary"
            size="sm"
            disabled={auditQuery.isFetchingNextPage}
            onClick={() => void auditQuery.fetchNextPage()}
          >
            {auditQuery.isFetchingNextPage ? t('common.loading') : t('audit.loadMore')}
          </Button>
        </div>
      )}

      {detailOf && (
        <Modal
          onClose={() => setDetailOf(null)}
          variant="center"
          size="lg"
          title={<span className="font-mono">{detailOf.action}</span>}
          description={`${new Date(detailOf.ts).toLocaleString(i18n.resolvedLanguage)}${detailOf.actorUsername ? ` · ${detailOf.actorUsername}` : ''}`}
          footer={
            <Button size="sm" variant="secondary" onClick={() => setDetailOf(null)}>
              {t('common.close')}
            </Button>
          }
        >
          <pre className="max-h-96 overflow-auto rounded-md bg-canvas p-3 font-mono text-xs whitespace-pre-wrap">
            {JSON.stringify(detailOf.detail, null, 2)}
          </pre>
        </Modal>
      )}
    </div>
  )
}
