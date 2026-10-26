// Mirror of logsSocket.ts: the session cookie travels on its own in the
// WebSocket handshake, no token needs to be passed in the URL. Unlike logs,
// this socket is bidirectional (see TerminalPanel.tsx): besides receiving the
// container's output, the typed bytes are sent to it as stdin along with the
// terminal resize events.
export function openExecSocket(
  clusterId: number,
  namespace: string,
  pod: string,
  container: string,
  command: string,
): WebSocket {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = new URL(
    `${protocol}//${window.location.host}/ws/clusters/${clusterId}/namespaces/${namespace}/pods/${pod}/exec`,
  )
  if (container) url.searchParams.set('container', container)
  if (command) url.searchParams.set('command', command)
  const ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'
  return ws
}
