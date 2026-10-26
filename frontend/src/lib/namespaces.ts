/**
 * Serialisation of the namespace filter, which travels as a comma-separated list
 * both in the UI URL (`?ns=a,b`) and in the API (`?namespace=a,b`).
 *
 * The comma is safe as a separator because a namespace name is a DNS-1123 label
 * (lowercase, digits and hyphens) and can never contain one. It was chosen over
 * the repeated parameter to avoid having to support multi-value in `useUrlState`
 * or configure axios' array serialiser, which generates
 * `namespace[]=a&namespace[]=b` — a form the Go backend does not read.
 */
export function joinNamespaces(namespaces: string[]): string {
  return namespaces.join(',')
}

/** Reads the selection. Drops empties: `?ns=` means "all". */
export function splitNamespaces(value: string): string[] {
  return value
    .split(',')
    .map((name) => name.trim())
    .filter(Boolean)
}

/** Query params of the API's namespace filter; none means "all namespaces". */
export function namespaceParams(namespaces: string[]): { namespace?: string } {
  return namespaces.length > 0 ? { namespace: joinNamespaces(namespaces) } : {}
}
