import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Maximize2, Minimize2 } from 'lucide-react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { openExecSocket } from '../api/execSocket'
import { cn } from '../lib/cn'

interface TerminalPanelProps {
  clusterId: number
  namespace: string
  pod: string
  containers: string[]
  onClose: () => void
  /** Alternative source (e.g. exec in a Docker container); pods/exec by default. */
  openSocket?: (container: string, shell: string) => WebSocket
  subtitle?: string
  /** No overlay: the terminal fills the container (Terminal tab of the detail). */
  inline?: boolean
}

// The command launched is always an interactive shell (equivalent to
// "kubectl exec -it <pod> -- <shell>"), not arbitrary one-off commands: once
// inside, the user types whatever they need. /bin/bash first because it is
// the desired default shell; /bin/sh remains as the alternative for
// minimal containers that do not have it installed.
const SHELL_OPTIONS = ['/bin/bash', '/bin/sh']

const DEFAULT_WIDTH = 768 // equivalent to the previous max-w-3xl, now adjustable
const MIN_WIDTH = 360

export function TerminalPanel({ clusterId, namespace, pod, containers, onClose, openSocket, subtitle, inline }: TerminalPanelProps) {
  const { t } = useTranslation()
  const [container, setContainer] = useState(containers[0] ?? '')
  const [shell, setShell] = useState(SHELL_OPTIONS[0])
  const [connected, setConnected] = useState(false)
  const [width, setWidth] = useState(DEFAULT_WIDTH)
  const [fullscreen, setFullscreen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)

  // Switching container or shell requires a new exec session (the command is
  // fixed when the connection opens) — just as LogsPanel reopens the socket
  // when its props change, this effect reopens the whole socket + terminal
  // when any of the dependencies changes. width/fullscreen are deliberately
  // NOT included here: resizing the panel must not reopen the terminal
  // session.
  useEffect(() => {
    if (!containerRef.current) return

    const term = new Terminal({
      convertEol: true,
      fontFamily: 'var(--font-mono)',
      fontSize: 13,
      theme: { background: '#000000' },
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(containerRef.current)
    fitAddon.fit()

    const ws = openSocket ? openSocket(container, shell) : openExecSocket(clusterId, namespace, pod, container, shell)
    const encoder = new TextEncoder()

    function sendResize() {
      fitAddon.fit()
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }))
      }
    }

    ws.onopen = () => {
      setConnected(true)
      sendResize()
    }
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        term.write(ev.data)
      } else {
        term.write(new Uint8Array(ev.data as ArrayBuffer))
      }
    }
    ws.onclose = () => setConnected(false)
    ws.onerror = () => setConnected(false)

    // Keystrokes go as BINARY frames: the server tells stdin (binary) apart
    // from resize control messages (text, JSON) by the WebSocket message
    // type, not by content — see internal/ws/exec.go. Sending with
    // ws.send(string) would produce a text frame and the server would
    // interpret it as a resize attempt.
    const dataDisposable = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(data))
    })
    const resizeDisposable = term.onResize(sendResize)
    window.addEventListener('resize', sendResize)

    // The user can resize the panel (dragging the edge or fullscreen);
    // instead of listening to those states directly (it would force the
    // session to reopen if they were in the effect's deps), the panel's real
    // size is observed and the terminal refitted when it changes. The PTY
    // side already tolerates bursts of resizes fine (terminalSizeQueue in
    // exec.go discards the previous size if it has not been consumed) — what
    // must be avoided is excessive DOM work on every pixel of the drag,
    // hence the requestAnimationFrame.
    let rafId: number | null = null
    const resizeObserver = new ResizeObserver(() => {
      if (rafId !== null) return
      rafId = requestAnimationFrame(() => {
        rafId = null
        sendResize()
      })
    })
    if (panelRef.current) resizeObserver.observe(panelRef.current)

    return () => {
      if (rafId !== null) cancelAnimationFrame(rafId)
      resizeObserver.disconnect()
      window.removeEventListener('resize', sendResize)
      dataDisposable.dispose()
      resizeDisposable.dispose()
      ws.close()
      term.dispose()
    }
    // openSocket deliberately left out of the deps (same reason as in LogsPanel).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterId, namespace, pod, container, shell])

  // Dragging the panel's left edge (anchored to the right of the screen) to
  // change its width. Disabled in fullscreen.
  function handleDragStart(e: ReactPointerEvent) {
    if (fullscreen) return
    e.preventDefault()
    function handleMove(moveEvent: PointerEvent) {
      const next = window.innerWidth - moveEvent.clientX
      setWidth(Math.min(Math.max(next, MIN_WIDTH), window.innerWidth - 40))
    }
    function handleUp() {
      window.removeEventListener('pointermove', handleMove)
      window.removeEventListener('pointerup', handleUp)
    }
    window.addEventListener('pointermove', handleMove)
    window.addEventListener('pointerup', handleUp)
  }

  // This panel does NOT use ui/Modal, and it is not an oversight: Escape is a
  // legitimate key inside a terminal (vim, less, any TUI), so closing on
  // Escape would break the user's session in the middle of a command. Nor
  // does it want an external focus trap: xterm manages the focus. For the
  // same reason it does not close on a backdrop click, which with an open
  // interactive session would be an expensive accident. It closes only with
  // its button.
  // Inline, fullscreen still exists (it leaves the flow with fixed); the
  // draggable width makes no sense and neither does the close button.

  const inlineFrame = inline && !fullscreen
  return (
    <div className={inlineFrame ? 'h-full min-h-80' : 'fixed inset-0 z-(--z-drawer) flex items-stretch justify-end bg-black/40'}>
      <div
        ref={panelRef}
        style={fullscreen || inlineFrame ? undefined : { width }}
        className={cn(
          'relative flex h-full flex-col bg-surface',
          inlineFrame ? 'overflow-hidden rounded-md border border-border' : 'shadow-xl',
          fullscreen && 'w-full',
        )}
      >
        {!fullscreen && !inline && (
          <div
            onPointerDown={handleDragStart}
            className="absolute top-0 left-0 z-10 h-full w-1.5 -translate-x-1/2 cursor-col-resize hover:bg-primary/30"
            title={t('terminal.resizeHint')}
          />
        )}
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
          <div>
            <h2 className="font-mono text-sm font-semibold">{pod}</h2>
            <p className="text-xs text-fg-muted">
              {subtitle ?? namespace} ·{' '}
              <span className={connected ? 'text-success' : 'text-danger'}>
                {connected ? t('terminal.connected') : t('terminal.disconnected')}
              </span>
            </p>
          </div>
          <div className="flex items-center gap-2">
            {containers.length > 1 && (
              <select
                value={container}
                onChange={(e) => setContainer(e.target.value)}
                className="rounded-md border border-border bg-surface px-2 py-1 text-xs"
              >
                {containers.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            )}
            <select
              value={shell}
              onChange={(e) => setShell(e.target.value)}
              className="rounded-md border border-border bg-surface px-2 py-1 text-xs"
            >
              {SHELL_OPTIONS.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={() => setFullscreen((f) => !f)}
              title={fullscreen ? t('terminal.exitFullscreen') : t('terminal.fullscreen')}
              className="rounded-md p-1.5 text-fg-muted hover:bg-canvas hover:text-fg"
            >
              {fullscreen ? (
                <Minimize2 className="h-4 w-4" aria-hidden="true" />
              ) : (
                <Maximize2 className="h-4 w-4" aria-hidden="true" />
              )}
            </button>
            {!inline && (
              <button
                type="button"
                onClick={onClose}
                className="rounded-md px-2 py-1 text-sm text-fg-muted hover:bg-canvas"
              >
                {t('common.close')}
              </button>
            )}
          </div>
        </div>
        <div ref={containerRef} className="min-h-0 flex-1 bg-black p-2" />
      </div>
    </div>
  )
}
