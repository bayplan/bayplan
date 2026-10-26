import { useCallback, useEffect, useRef, useState } from 'react'

const COPIED_MS = 1500

/**
 * Copy to the clipboard with a short-lived "copied" flag. Replaces six local
 * copies of the same function, one of which did not catch the rejection of
 * `navigator.clipboard` (plain-HTTP origins, permission denied).
 *
 * `copiedKey` tells which of several buttons of the same component copied
 * (e.g. the webhook URL or its token); `copied` is enough when there is one.
 */
export function useCopy() {
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  useEffect(() => () => clearTimeout(timer.current), [])

  const copy = useCallback(async (text: string, key = 'default'): Promise<boolean> => {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      // Clipboard not available: the text stays visible to select by hand.
      return false
    }
    setCopiedKey(key)
    clearTimeout(timer.current)
    timer.current = setTimeout(() => setCopiedKey(null), COPIED_MS)
    return true
  }, [])

  return { copy, copied: copiedKey !== null, copiedKey }
}
