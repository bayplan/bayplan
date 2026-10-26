/** Opens a cluster's kubectl web shell (see internal/ws/kubectl.go). */
export function openKubectlSocket(clusterId: number, namespace?: string): WebSocket {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = new URL(`${protocol}//${window.location.host}/ws/clusters/${clusterId}/kubectl`)
  if (namespace) url.searchParams.set('namespace', namespace)
  const ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'
  return ws
}
