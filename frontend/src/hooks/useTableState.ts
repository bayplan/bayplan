import { useCallback } from 'react'
import type { DataTableState, SortDir } from '../components/ui'
import { useUrlState } from './useUrlState'
import { usePreferences } from './usePreferences'

// Default page size. 50 and not 100 because the tables
// refresh every 7 s from the informer cache: fewer rows in the DOM is less
// work on every refetch, and with 50 the pagination actually shows up in
// real listings instead of always staying hidden.
const DEFAULT_PAGE_SIZE = 50

const TABLE_KEYS = ['sort', 'dir', 'page'] as const

/**
 * Sorting and pagination of a table, in the query string. Same criterion as
 * the filters (see useUrlState): the view sorted and on page N is a shareable
 * link and survives a reload.
 *
 * The sort order and the page live in the URL too, not only the text filter:
 * otherwise going back would only recover the state halfway.
 */
export function useTableState(options?: {
  sortKey?: string
  sortDir?: SortDir
  pageSize?: number
}): [DataTableState, (patch: Partial<DataTableState>) => void] {
  const [params, setParams] = useUrlState(TABLE_KEYS)
  const { preferences } = usePreferences()

  // Priority: what the page explicitly asks for, then the user's preference,
  // and the constant only as a last resort.
  const pageSize = options?.pageSize ?? preferences.rowsPerPage ?? DEFAULT_PAGE_SIZE
  // A non-numeric `page` or < 1 (hand-written URL) is treated as the first
  // page; the upper bound is clamped by the table itself, which is the only
  // one that knows how many rows there are after filtering.
  const parsedPage = Number(params.page)
  const state: DataTableState = {
    sortKey: params.sort || (options?.sortKey ?? ''),
    sortDir: params.dir === 'desc' || params.dir === 'asc' ? params.dir : (options?.sortDir ?? 'asc'),
    page: Number.isInteger(parsedPage) && parsedPage > 0 ? parsedPage : 1,
    pageSize,
  }

  const setState = useCallback(
    (patch: Partial<DataTableState>) => {
      setParams({
        ...(patch.sortKey !== undefined ? { sort: patch.sortKey } : {}),
        ...(patch.sortDir !== undefined ? { dir: patch.sortDir } : {}),
        // Page 1 is not written to the URL: it is the default value and
        // "?page=1" only adds noise to a link that is going to be shared.

        ...(patch.page !== undefined ? { page: patch.page > 1 ? String(patch.page) : '' } : {}),
      })
    },
    [setParams],
  )

  return [state, setState]
}
