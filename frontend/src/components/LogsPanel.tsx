import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowDownToLine } from 'lucide-react'
import { openLogsSocket } from '../api/logsSocket'
import { Button, Modal } from './ui'

interface LogsPanelProps {
  clusterId: number
  namespace: string
  pod: string
  container: string
  onClose: () => void
  /** Alternative stream source (e.g. a Docker container); pod logs by default. */
  openSocket?: () => WebSocket
  subtitle?: string
  /**
   * No overlay: the stream is rendered inside the page (Logs tab of a pod's
   * detail) and takes the container's height. `onClose` is ignored.
   */
  inline?: boolean
}

// Margin from the bottom within which the user is still considered to be
// "at the bottom": a scroll halfway through a line must not pause following.
const FOLLOW_THRESHOLD_PX = 8

export function LogsPanel({ clusterId, namespace, pod, container, onClose, openSocket, subtitle, inline }: LogsPanelProps) {
  const { t } = useTranslation()
  const [lines, setLines] = useState<string[]>([])
  const [connected, setConnected] = useState(false)
  // Auto-scroll to the end with every new line. It is only disabled if the
  // user scrolls up to read something (before, every line dragged them back to
  // the bottom) and re-enabled with the button.
  const [follow, setFollow] = useState(true)
  const outputRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    // A new stream starts from a clean slate: otherwise switching containers
    // appended the second one's logs to the first's, as if they were one.
    setLines([])
    setFollow(true)
    const ws = openSocket ? openSocket() : openLogsSocket(clusterId, namespace, pod, container)
    ws.onopen = () => setConnected(true)
    ws.onmessage = (ev) => {
      setLines((prev) => [...prev, String(ev.data)])
    }
    ws.onclose = () => setConnected(false)
    ws.onerror = () => setConnected(false)
    return () => ws.close()
    // openSocket is deliberately excluded: it is a new function on every render
    // and would reopen the stream continuously; the resource identity is
    // already in the other dependencies.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterId, namespace, pod, container])

  useEffect(() => {
    const el = outputRef.current
    if (!follow || !el) return
    el.scrollTop = el.scrollHeight
  }, [lines, follow])

  function handleScroll() {
    const el = outputRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= FOLLOW_THRESHOLD_PX
    // The programmatic scroll to the bottom also fires this event, but leaves
    // `atBottom` true, so only a manual scroll upwards pauses.

    if (!atBottom && follow) setFollow(false)
  }

  const status = (
    <span className={connected ? 'text-success' : 'text-danger'}>
      {connected ? t('logs.connected') : t('logs.disconnected')}
    </span>
  )
  const followButton = (
    <>
      {!follow && <span className="mr-auto self-center text-xs text-fg-muted">{t('logs.paused')}</span>}
      <Button size="sm" variant={follow ? 'primary' : 'secondary'} aria-pressed={follow} onClick={() => setFollow((f) => !f)}>
        <ArrowDownToLine aria-hidden="true" />
        {t('logs.follow')}
      </Button>
    </>
  )
  const output = (
    <pre
      ref={outputRef}
      onScroll={handleScroll}
      className="min-h-0 flex-1 overflow-y-auto bg-black p-4 font-mono text-xs whitespace-pre-wrap break-all text-green-400"
    >
      {lines.join('')}
    </pre>
  )

  if (inline) {
    return (
      <div className="flex h-full min-h-80 flex-col overflow-hidden rounded-md border border-border">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-surface px-3 py-2 text-xs text-fg-muted">
          <span>
            {subtitle ?? `${namespace} · ${container || t('logs.defaultContainer')}`} · {status}
          </span>
          <span className="flex items-center gap-2">{followButton}</span>
        </div>
        {output}
      </div>
    )
  }

  return (
    <Modal
      onClose={onClose}
      title={<span className="font-mono">{pod}</span>}
      description={
        <>
          {subtitle ?? `${namespace} · ${container || t('logs.defaultContainer')}`} · {status}
        </>
      }
      bodyClassName="flex flex-col overflow-hidden p-0"
      footer={followButton}
    >
      {output}
    </Modal>
  )
}
