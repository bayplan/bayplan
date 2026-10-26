import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { ChevronDown, ChevronUp, ChevronsUpDown } from 'lucide-react'
import { Alert } from './Alert'
import { cn } from '../../lib/cn'

export type SortDir = 'asc' | 'desc'

export interface DataTableColumn<T> {
  key: string
  header: ReactNode
  render: (row: T) => ReactNode
  cellClassName?: string
  headerClassName?: string
  /**
   * Value this column sorts by. A column WITHOUT sortValue is not sortable:
   * `render` returns a ReactNode, which cannot be compared, so sorting has to
   * be declared, not guessed.
   */
  sortValue?: (row: T) => string | number
  /**
   * Column that stays visible on narrow screens too. If any column declares
   * it, the others are hidden on mobile (instead of a horizontal scroll of
   * twelve columns); if none declares it, all are shown with scrolling.
   */
  primary?: boolean
}

/** Sort and pagination state. Owned by the page (see hooks/useTableState). */
export interface DataTableState {
  sortKey: string
  sortDir: SortDir
  page: number
  pageSize: number
}

interface DataTableProps<T> {
  columns: DataTableColumn<T>[]
  data: T[]
  rowKey: (row: T) => string
  /**
   * "Nothing to show" message. Defaults to `common.noData`; only pass it when
   * the empty case deserves an explanation of its own.
   */
  emptyMessage?: ReactNode
  /**
   * true when a filter is active. Swaps the empty message for
   * `common.noResults`: "there are no pods" and "your search finds no pods"
   * are two different situations and they used to look the same.
   */
  filtered?: boolean
  /**
   * First load in progress. Without this, "loading" and "empty" looked
   * exactly the same: pages pass `data ?? []` and the table showed the
   * empty-table message while the request was still in flight.
   */
  isLoading?: boolean
  /**
   * Load error. Without this the table said "no data" when the query failed,
   * which is the opposite of what is happening.
   */
  error?: ReactNode
  /** Per-row actions menu (normally `<RowActions>`), in the last column. */
  actions?: (row: T) => ReactNode
  /**
   * Destination when clicking anywhere on the row (the resource detail).
   * A click on a link, button or control inside the row does not navigate:
   * it already does its own thing. With this the name stops being the only
   * "live" spot of the row.
   */
  rowHref?: (row: T) => string | undefined
  state?: DataTableState
  onStateChange?: (patch: Partial<DataTableState>) => void
}

// String comparison with localeCompare and `numeric`, not with `<`: that way
// "pod-2" goes before "pod-10" (and not the other way round, which is what
// lexicographic order does) and accented letters sort as a Spanish reader expects.
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

function compare(a: string | number, b: string | number): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return collator.compare(String(a), String(b))
}

// Consolidates the ~7 nearly identical tables scattered across ClusterView,
// ClusterAdmin and UserAdmin (same thead/tbody/border/hover, only the columns
// changed). With this, a table style change is applied in a single place
// instead of N.
//
// Sorting and pagination are CONTROLLED (state + onStateChange) instead of
// internal state: they live in the query string so the view is a shareable
// link (see hooks/useUrlState), and only the page can know that. Without
// `state` the table behaves as before: no sorting and no pagination.
export function DataTable<T>({
  columns,
  data,
  rowKey,
  emptyMessage,
  filtered,
  isLoading,
  error,
  actions,
  rowHref,
  state,
  onStateChange,
}: DataTableProps<T>) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const hasPrimary = columns.some((c) => c.primary)
  const hiddenOnMobile = (col: DataTableColumn<T>) => hasPrimary && !col.primary && 'max-md:hidden'
  // On mobile the first `primary` column (the name) absorbs the spare width
  // and wraps the text: `max-w-0 w-full` is the trick so an `auto` table does
  // not grow past its container because of a long cell, and so the kebab
  // always stays in view.
  const firstPrimary = columns.find((c) => c.primary)?.key
  const stretchOnMobile = (col: DataTableColumn<T>) => hasPrimary && col.key === firstPrimary && 'max-md:w-full max-md:max-w-0 max-md:break-all'

  const sortColumn = state?.sortKey ? columns.find((c) => c.key === state.sortKey) : undefined
  const sortValue = sortColumn?.sortValue
  // Without a valid sort column the order the data arrives in is respected.
  // It matters: the backend sorts events by lastSeen desc before truncating
  // to 200, and re-sorting them here by default would misrepresent that cut.
  const sorted = sortValue
    ? [...data].sort((a, b) => {
        const result = compare(sortValue(a), sortValue(b))
        return state?.sortDir === 'desc' ? -result : result
      })
    : data

  const pageSize = state?.pageSize ?? 0
  const totalPages = pageSize > 0 ? Math.max(1, Math.ceil(sorted.length / pageSize)) : 1
  // Clamped at render time instead of correcting the state: if the filter
  // leaves fewer pages than there were, writing the page to the URL from the
  // render would loop with the navigation effect itself.
  const page = Math.min(Math.max(state?.page ?? 1, 1), totalPages)
  const visible = pageSize > 0 ? sorted.slice((page - 1) * pageSize, page * pageSize) : sorted

  function toggleSort(column: DataTableColumn<T>) {
    if (!column.sortValue || !onStateChange) return
    const isCurrent = state?.sortKey === column.key
    // Switching column always starts ascending; on the same column it
    // toggles. Changing the order goes back to page 1: staying on page 3
    // after re-sorting leaves the user on a slice they never asked for.
    onStateChange({
      sortKey: column.key,
      sortDir: isCurrent && state?.sortDir === 'asc' ? 'desc' : 'asc',
      page: 1,
    })
  }

  const columnCount = columns.length + (actions ? 1 : 0)

  return (
    <div className="min-w-0">
      {/* `overflow-x-clip` and not `auto`: clip does not create a scroll container,
          so the header can be sticky relative to <main> (the only scroll) and
          the block stops imposing its minimum width on a parent grid (the table
          fits and cells wrap their text). On mobile without `primary` columns
          horizontal scrolling is allowed. */}
      <div className={cn('min-w-0 rounded-md border border-border bg-surface', hasPrimary ? 'overflow-x-clip' : 'overflow-x-auto md:overflow-x-clip')}>
        <table className="w-full text-left text-sm">
          <thead className="sticky top-0 z-(--z-sticky) bg-surface shadow-[inset_0_-1px_0_var(--color-border)]">
            <tr>
              {columns.map((col) => {
                const sortable = !!col.sortValue && !!onStateChange
                const isCurrent = state?.sortKey === col.key
                const SortIcon = !isCurrent ? ChevronsUpDown : state?.sortDir === 'desc' ? ChevronDown : ChevronUp
                return (
                  <th
                    key={col.key}
                    // aria-sort on the th is what announces the order to a screen
                    // reader; the icon only conveys it visually.
                    aria-sort={isCurrent ? (state?.sortDir === 'desc' ? 'descending' : 'ascending') : undefined}
                    className={cn('px-3 py-2 text-xs font-medium tracking-wide text-fg-muted uppercase', hiddenOnMobile(col), stretchOnMobile(col), col.headerClassName)}
                  >
                    {sortable ? (
                      <button
                        type="button"
                        onClick={() => toggleSort(col)}
                        className={`flex items-center gap-1 uppercase transition-colors hover:text-fg ${
                          isCurrent ? 'text-fg' : ''
                        }`}
                      >
                        {col.header}
                        <SortIcon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                      </button>
                    ) : (
                      col.header
                    )}
                  </th>
                )
              })}
              {actions && (
                <th className="w-24 px-2 py-2 text-right text-xs font-medium tracking-wide text-fg-muted uppercase">
                  {t('common.actions')}
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {visible.map((row) => (
              <tr
                key={rowKey(row)}
                className={cn('border-t border-border transition-colors hover:bg-surface-hover', rowHref?.(row) && 'cursor-pointer')}
                onClick={(e) => {
                  const href = rowHref?.(row)
                  if (!href) return
                  // Only a "clean" click on the row: not on a control, not while
                  // selecting text, and not with modifiers (open in a new tab).
                  if ((e.target as HTMLElement).closest('a, button, input, select, textarea, label, [role=menu], [role=menuitem]')) return
                  if (e.metaKey || e.ctrlKey || e.shiftKey || window.getSelection()?.toString()) return
                  navigate(href)
                }}
              >
                {columns.map((col) => (
                  // Vertical padding comes from the density chosen in the profile
                  // (--table-cell-py, see index.css), not from a fixed class.
                  <td key={col.key} className={cn('px-3 py-[var(--table-cell-py)]', hiddenOnMobile(col), stretchOnMobile(col), col.cellClassName)}>
                    {col.render(row)}
                  </td>
                ))}
                {actions && <td className="px-2 py-[var(--table-cell-py)] text-right">{actions(row)}</td>}
              </tr>
            ))}
            {visible.length === 0 && (
              <tr>
                <td colSpan={columnCount} className="px-3 py-8 text-center text-fg-muted">
                  {error ? (
                    <Alert tone="danger" className="mx-auto max-w-xl text-left">
                      {error}
                    </Alert>
                  ) : isLoading ? (
                    t('common.loading')
                  ) : filtered ? (
                    t('common.noResults')
                  ) : (
                    (emptyMessage ?? t('common.noData'))
                  )}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* Pagination only shows when there is more than one page: in small tables
          (clusters, users) it would be a useless footer taking up space. */}
      {pageSize > 0 && totalPages > 1 && onStateChange && (
        <div className="sticky bottom-0 -mx-1 mt-2 flex items-center justify-between gap-3 bg-canvas px-1 py-1 text-xs text-fg-muted">
          <span>
            {t('table.range', {
              from: (page - 1) * pageSize + 1,
              to: Math.min(page * pageSize, sorted.length),
              total: sorted.length,
            })}
          </span>
          <div className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => onStateChange({ page: page - 1 })}
              disabled={page <= 1}
              className="rounded-md px-2 py-1 hover:bg-surface-hover hover:text-fg disabled:pointer-events-none disabled:opacity-40"
            >
              {t('table.previous')}
            </button>
            <span className="px-1">{t('table.pageOf', { page, total: totalPages })}</span>
            <button
              type="button"
              onClick={() => onStateChange({ page: page + 1 })}
              disabled={page >= totalPages}
              className="rounded-md px-2 py-1 hover:bg-surface-hover hover:text-fg disabled:pointer-events-none disabled:opacity-40"
            >
              {t('table.next')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
