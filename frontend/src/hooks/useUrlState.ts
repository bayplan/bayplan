import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'

// Syncs UI state (filters, search) with the query string. Before, all of this
// lived in useState: reloading the page lost it and there was no way to link
// to "the pods of namespace X of this cluster".
//
// Three decisions that should not be reverted without thinking:
//
//   - Navigation uses `replace`, not `push`. With `push`, every keystroke in
//     the search box would leave a history entry and the back button would
//     stop being useful for returning to the previous screen.
//
//   - An empty value is DELETED from the parameter instead of being written
//     as "?q=". That way "no filter" has a single representation in the URL,
//     and not two that would have to be treated the same wherever they are
//     read.
//
//   - It is always read from the URL, with no copy in useState. A local copy
//     would have to be resynced with the back button and with every route
//     change, which is exactly the kind of bug this hook exists to avoid.
//
// Single-element values only: the multi-namespace picker will need
// getAll()/append() and is a separate change.
export function useUrlState<K extends string>(
  keys: readonly K[],
): [Record<K, string>, (patch: Partial<Record<K, string>>) => void] {
  const [searchParams, setSearchParams] = useSearchParams()

  // Deliberately no useMemo: building an object of half a dozen strings on
  // every render is cheaper than the trap of having to keep the identity of
  // the `keys` array stable at every call site of the hook.
  const values = {} as Record<K, string>
  for (const key of keys) values[key] = searchParams.get(key) ?? ''

  const setValues = useCallback(
    (patch: Partial<Record<K, string>>) => {
      setSearchParams(
        // Functional update: the patch is applied over the parameters present
        // at that moment, not over those of the render in which the handler
        // was created. Without this, two consecutive changes (or a change
        // while a render is pending) overwrite each other.
        (prev) => {
          const next = new URLSearchParams(prev)
          // Object.keys + indexing, not Object.entries: over a Partial<Record<K,
          // string>> the entries value comes out as `{} | null` and does not compile.

          for (const key of Object.keys(patch) as K[]) {
            const value = patch[key]
            if (value === undefined) continue
            if (value === '') next.delete(key)
            else next.set(key, value)
          }
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  return [values, setValues]
}
