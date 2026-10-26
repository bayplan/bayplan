// The browser sends the session cookie automatically in the WebSocket
// handshake (same httpOnly cookie as the rest of the API): no token needs
// to be passed in the URL.
export function openLogsSocket(
  clusterId: number,
  namespace: string,
  pod: string,
  container: string,
): WebSocket {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = new URL(
    `${protocol}//${window.location.host}/ws/clusters/${clusterId}/namespaces/${namespace}/pods/${pod}/logs`,
  )
  if (container) url.searchParams.set('container', container)
  return new WebSocket(url)
}
