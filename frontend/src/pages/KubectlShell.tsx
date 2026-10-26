import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { listClusters } from '../api/clusters'
import { openKubectlSocket } from '../api/kubectlSocket'
import { Alert, Badge, PageHeader } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { splitNamespaces } from '../lib/namespaces'
import { NotFound } from './NotFound'

// Banner lines shown on connect and on `help`; translated at write time so the
// shell follows the UI language.
const HELP_KEYS = ['kubectl.help1', 'kubectl.help2', 'kubectl.help3', 'kubectl.help4']

/**
 * kubectl shell in the browser. The line editor lives here (local echo,
 * history, Ctrl+C/L/D) and every Enter sends the line to the server, which validates
 * it and runs kubectl in a PTY; while a command is running, the keyboard goes raw
 * to the process (exec -it, logs -f work like in a real terminal).
 */
export function KubectlShell() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const clusterId = Number(id)
  const validCluster = Number.isInteger(clusterId) && clusterId > 0
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: listClusters })
  const cluster = clustersQuery.data?.find((c) => c.id === clusterId)
  const isAdmin = cluster?.role === 'admin'
  const [filters] = useUrlState(['ns'] as const)
  const namespaces = splitNamespaces(filters.ns)
  // A single namespace sets the kubectl context; with several or none, default.
  const namespace = namespaces.length === 1 ? namespaces[0] : undefined
  const [status, setStatus] = useState<'connecting' | 'ready' | 'closed' | 'error'>('connecting')
  const [errorText, setErrorText] = useState<string | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!validCluster || !isAdmin || !containerRef.current) return
    const term = new Terminal({ convertEol: true, fontFamily: 'var(--font-mono)', fontSize: 13, cursorBlink: true, theme: { background: '#000000' } })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()

    const ws = openKubectlSocket(clusterId, namespace)
    const encoder = new TextEncoder()
    const prompt = () => term.write(`\r\n\x1b[1;32m${namespace ?? 'default'}\x1b[0m $ `)

    let running = false
    let line = ''
    let cursor = 0
    const history: string[] = []
    let histIdx = -1
    let draft = ''

    function redraw() {
      // Repaint the whole line: erase to the end and write it again.
      term.write('\r\x1b[2K' + `\x1b[1;32m${namespace ?? 'default'}\x1b[0m $ ` + line)
      const back = line.length - cursor
      if (back > 0) term.write(`\x1b[${back}D`)
    }
    function sendResize() {
      fit.fit()
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }))
    }
    function run(l: string) {
      const trimmed = l.trim()
      if (trimmed) {
        history.push(trimmed)
        if (history.length > 200) history.shift()
      }
      histIdx = -1
      draft = ''
      // The line is cleared here, also for the local builtins: if it were only
      // cleared when a remote command finishes, "clear" would leave its text
      // in the buffer and it would get glued to the next command.
      line = ''
      cursor = 0
      term.write('\r\n')
      switch (trimmed) {
        case '':
          prompt()
          return
        case 'clear':
          term.clear()
          prompt()
          return
        case 'help':
          HELP_KEYS.forEach((k) => term.writeln(t(k)))
          prompt()
          return
        case 'exit':
          ws.close()
          return
      }
      running = true
      ws.send(JSON.stringify({ type: 'run', line: trimmed }))
    }

    ws.onopen = () => {
      setStatus('ready')
      sendResize()
    }
    ws.onmessage = (ev) => {
      if (typeof ev.data !== 'string') {
        term.write(new Uint8Array(ev.data as ArrayBuffer))
        return
      }
      let msg: { type?: string; code?: number; message?: string; cluster?: string } = {}
      try {
        msg = JSON.parse(ev.data)
      } catch {
        term.write(ev.data)
        return
      }
      if (msg.type === 'ready') {
        term.writeln(`\x1b[2m${t('kubectl.banner', { cluster: msg.cluster ?? '' })}\x1b[0m`)
        HELP_KEYS.forEach((k) => term.writeln(`\x1b[2m${t(k)}\x1b[0m`))
        prompt()
      } else if (msg.type === 'error') {
        term.writeln(`\x1b[31m${msg.message ?? ''}\x1b[0m`)
      } else if (msg.type === 'done') {
        running = false
        line = ''
        cursor = 0
        if (msg.code && msg.code !== 0) term.write(`\x1b[2m[exit ${msg.code}]\x1b[0m`)
        prompt()
      }
    }
    ws.onclose = () => {
      setStatus((s) => (s === 'error' ? s : 'closed'))
      term.writeln(`\r\n\x1b[2m${t('kubectl.closed')}\x1b[0m`)
    }
    ws.onerror = () => {
      setStatus('error')
      setErrorText(t('kubectl.unavailable'))
    }

    const dataDisposable = term.onData((data) => {
      if (ws.readyState !== WebSocket.OPEN) return
      if (running) {
        ws.send(encoder.encode(data))
        return
      }
      // Local line editor.
      switch (data) {
        case '\r':
          run(line)
          return
        case '\u007f': // backspace
          if (cursor > 0) {
            line = line.slice(0, cursor - 1) + line.slice(cursor)
            cursor--
            redraw()
          }
          return
        case '\u0003': // Ctrl+C
          term.write('^C')
          line = ''
          cursor = 0
          prompt()
          return
        case '\u0004': // Ctrl+D
          if (line === '') ws.close()
          return
        case '\u000c': // Ctrl+L
          term.clear()
          redraw()
          return
        case '\u0001': // Ctrl+A
          cursor = 0
          redraw()
          return
        case '\u0005': // Ctrl+E
          cursor = line.length
          redraw()
          return
        case '\u0015': // Ctrl+U
          line = line.slice(cursor)
          cursor = 0
          redraw()
          return
        case '\x1b[A': // ↑
          if (history.length === 0) return
          if (histIdx === -1) {
            draft = line
            histIdx = history.length - 1
          } else if (histIdx > 0) histIdx--
          line = history[histIdx]
          cursor = line.length
          redraw()
          return
        case '\x1b[B': // ↓
          if (histIdx === -1) return
          if (histIdx < history.length - 1) {
            histIdx++
            line = history[histIdx]
          } else {
            histIdx = -1
            line = draft
          }
          cursor = line.length
          redraw()
          return
        case '\x1b[C': // →
          if (cursor < line.length) {
            cursor++
            term.write(data)
          }
          return
        case '\x1b[D': // ←
          if (cursor > 0) {
            cursor--
            term.write(data)
          }
          return
      }
      if (data.startsWith('\x1b')) return // other escape sequences
      // Paste or normal typing: printable characters only.
      const clean = Array.from(data)
        .filter((ch) => ch.charCodeAt(0) >= 32)
        .join('')
      if (!clean) return
      line = line.slice(0, cursor) + clean + line.slice(cursor)
      cursor += clean.length
      redraw()
    })
    const resizeDisposable = term.onResize(sendResize)
    window.addEventListener('resize', sendResize)
    // The FIXED-height container is observed (not the terminal's): if the box
    // grew with the rows, fit() would add rows, the box would grow and so on
    // until the tab hangs. The rAF batches bursts of resizes.
    let rafId: number | null = null
    const observer = new ResizeObserver(() => {
      if (rafId !== null) return
      rafId = requestAnimationFrame(() => {
        rafId = null
        sendResize()
      })
    })
    observer.observe(containerRef.current)
    term.focus()

    return () => {
      if (rafId !== null) cancelAnimationFrame(rafId)
      observer.disconnect()
      window.removeEventListener('resize', sendResize)
      dataDisposable.dispose()
      resizeDisposable.dispose()
      ws.close()
      term.dispose()
    }
    // t is left out of the deps: switching language must not reopen the session.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterId, namespace, validCluster, isAdmin])

  if (!validCluster || (clustersQuery.data && !cluster)) return <NotFound />

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={t('kubectl.title')}
        description={t('kubectl.hint')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: cluster?.displayName ?? t('cluster.title'), to: `/clusters/${clusterId}` }, { label: t('kubectl.title') }]}
        actions={
          <Badge tone={status === 'ready' ? 'success' : status === 'connecting' ? 'warning' : 'neutral'}>
            {t(`kubectl.status.${status}`)}
          </Badge>
        }
      />
      {cluster && !isAdmin && <p className="text-sm text-fg-muted">{t('kubectl.adminOnly')}</p>}
      {errorText && (
        <Alert tone="danger" className="mb-2">
          {errorText}
        </Alert>
      )}
      {isAdmin && (
        <div className="min-h-0 flex-1 overflow-hidden rounded-md bg-black p-2">
          <div ref={containerRef} className="h-full w-full" />
        </div>
      )}
    </div>
  )
}
