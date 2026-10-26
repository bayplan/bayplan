/**
 * Formatting of Kubernetes quantities. Lives in the frontend and not in the
 * backend on purpose: the backend sends raw numbers (millicores, bytes) and the
 * UI is what knows in which language and with which decimal separator to paint
 * them.
 */

/** Millicores to text: below one core it is shown in "m", like kubectl. */
export function formatCpu(milli: number, locale: string): string {
  if (milli < 1000) return `${milli} m`
  return `${(milli / 1000).toLocaleString(locale, { maximumFractionDigits: 1 })} cores`
}

// Binary units (KiB, MiB…) and not decimal: they are the ones Kubernetes uses
// in requests and the ones that appear in any manifest.
const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

export function formatBytes(bytes: number, locale: string): string {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toLocaleString(locale, { maximumFractionDigits: value < 10 ? 1 : 0 })} ${BYTE_UNITS[unit]}`
}

/** Percentage clamped to [0, 100]; 0 if the total is 0, to avoid dividing by zero. */
export function percent(used: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, (used / total) * 100))
}
