import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronUp, Eraser, Maximize2, Minimize2, Minus, Send, Sparkles, Square, Wrench, X } from 'lucide-react'
import { chat, type AssistantEvent, type AssistantMessage } from '../api/assistant'
import { AnswerText } from './ExplainButton'
import { Button } from './ui'
import { usePreferences } from '../hooks/usePreferences'
import { cn } from '../lib/cn'

const DEFAULT_HEIGHT = 380
const MIN_HEIGHT = 140
// What is always left for the page above when dragging: the panel never
// swallows it by accident (maximize does that on purpose).
const PAGE_RESERVE = 96

type Mode = 'normal' | 'min' | 'max'

interface Turn {
  question: string
  activity: string[]
  answer?: string
  error?: string
  usage?: string
}

/**
 * The in-panel AI assistant: a bottom panel that answers questions about the
 * clusters with the provider configured in Settings. Read-only (the server
 * only gives the model read tools) and with no history beyond this panel:
 * closing it forgets the conversation, nothing is stored on the server.
 */
export function AssistantPanel({ clusterId, namespaces, onClose }: { clusterId?: number; namespaces: string[]; onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const [turns, setTurns] = useState<Turn[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const abort = useRef<AbortController | null>(null)
  const bottom = useRef<HTMLDivElement>(null)
  const { preferences, update: savePreferences } = usePreferences()
  // Minimized keeps the conversation (the component stays mounted); only
  // closing forgets it.
  const [mode, setMode] = useState<Mode>('normal')
  const [height, setHeight] = useState(preferences.assistantHeight || DEFAULT_HEIGHT)
  const section = useRef<HTMLElement>(null)
  const drag = useRef<{ startY: number; startHeight: number } | null>(null)

  const maxHeight = () => (section.current?.parentElement?.clientHeight ?? window.innerHeight) - PAGE_RESERVE
  const clamp = (h: number) => Math.max(MIN_HEIGHT, Math.min(maxHeight(), Math.round(h)))
  const commitHeight = (h: number) => {
    setHeight(h)
    void savePreferences({ assistantHeight: h })
  }

  function onResizeStart(e: PointerEvent<HTMLDivElement>) {
    if (mode !== 'normal') setMode('normal')
    e.currentTarget.setPointerCapture(e.pointerId)
    drag.current = { startY: e.clientY, startHeight: section.current?.clientHeight ?? height }
  }
  function onResizeMove(e: PointerEvent<HTMLDivElement>) {
    if (!drag.current) return
    setHeight(clamp(drag.current.startHeight + drag.current.startY - e.clientY))
  }
  function onResizeEnd(e: PointerEvent<HTMLDivElement>) {
    if (!drag.current) return
    drag.current = null
    e.currentTarget.releasePointerCapture(e.pointerId)
    commitHeight(height)
  }
  function onResizeKey(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return
    e.preventDefault()
    setMode('normal')
    commitHeight(clamp(height + (e.key === 'ArrowUp' ? 32 : -32)))
  }

  // Braces on purpose: recent browsers return a Promise from scrollIntoView,
  // and React would call it as the cleanup on unmount (blank page on close).
  useEffect(() => {
    bottom.current?.scrollIntoView({ block: 'end' })
  }, [turns])
  useEffect(() => () => abort.current?.abort(), [])

  const update = (fn: (turn: Turn) => Turn) => setTurns((all) => [...all.slice(0, -1), fn(all[all.length - 1])])

  async function ask(question: string) {
    const q = question.trim()
    if (!q || busy) return
    // Only finished turns go back as history: an errored one has no answer
    // and the server requires alternating roles ending with the question.
    const history: AssistantMessage[] = []
    for (const turn of turns) {
      if (turn.answer) history.push({ role: 'user', content: turn.question }, { role: 'assistant', content: turn.answer })
    }
    history.push({ role: 'user', content: q })
    setTurns((all) => [...all, { question: q, activity: [] }])
    setInput('')
    setBusy(true)
    const controller = new AbortController()
    abort.current = controller
    const onEvent = (e: AssistantEvent) => {
      switch (e.type) {
        case 'tool':
          update((turn) => ({ ...turn, activity: [...turn.activity, describeTool(e.name, e.args)] }))
          break
        case 'tool_error':
          update((turn) => ({ ...turn, activity: [...turn.activity, `${e.name}: ${e.message}`] }))
          break
        case 'answer':
          update((turn) => ({ ...turn, answer: e.text }))
          break
        case 'done':
          update((turn) => ({ ...turn, usage: t('assistant.usage', { tokens: e.inputTokens + e.outputTokens, remaining: e.remainingToday, count: e.remainingToday, model: e.model }) }))
          break
        case 'error':
          update((turn) => ({ ...turn, error: e.message }))
          break
      }
    }
    try {
      await chat({ messages: history, clusterId, namespaces, lang: i18n.resolvedLanguage ?? 'en' }, onEvent, controller.signal)
    } catch (err) {
      if (!controller.signal.aborted) update((turn) => ({ ...turn, error: err instanceof Error ? err.message : String(err) }))
      else update((turn) => ({ ...turn, error: t('assistant.stopped') }))
    } finally {
      setBusy(false)
      abort.current = null
      // The status carries what is left of the daily limit.
      void queryClient.invalidateQueries({ queryKey: ['ai-status'] })
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      void ask(input)
    }
  }

  const suggestions = clusterId ? [t('assistant.suggest.failing'), t('assistant.suggest.restarts'), t('assistant.suggest.capacity')] : [t('assistant.suggest.fleet'), t('assistant.suggest.failing')]

  // Terminal look on purpose, dark in both themes: this panel talks to a
  // third party and should never be mistaken for the app's own screens.
  const termButton = 'text-term-muted hover:bg-term-surface hover:text-term-fg disabled:opacity-40'
  return (
    <section
      ref={section}
      aria-label={t('assistant.title')}
      style={mode === 'normal' ? { height, maxHeight: `calc(100% - ${PAGE_RESERVE}px)` } : mode === 'max' ? { height: '100%' } : undefined}
      className="relative flex shrink-0 flex-col border-t border-term-border bg-term-bg font-mono text-[13px] text-term-fg"
    >
      {mode !== 'min' && (
        <div
          role="separator"
          aria-orientation="horizontal"
          aria-label={t('assistant.resize')}
          aria-valuenow={height}
          tabIndex={0}
          onPointerDown={onResizeStart}
          onPointerMove={onResizeMove}
          onPointerUp={onResizeEnd}
          onPointerCancel={onResizeEnd}
          onDoubleClick={() => setMode(mode === 'max' ? 'normal' : 'max')}
          onKeyDown={onResizeKey}
          className="absolute inset-x-0 -top-1 z-10 h-2 cursor-row-resize touch-none hover:bg-term-prompt/40 focus-visible:bg-term-prompt/40"
        />
      )}
      <div
        className={cn('flex items-center gap-2 bg-term-surface px-4 py-1.5', mode !== 'min' && 'border-b border-term-border')}
        onDoubleClick={() => setMode(mode === 'min' ? 'normal' : 'min')}
      >
        <Sparkles className="text-term-prompt" aria-hidden="true" />
        <h2 className="text-xs font-semibold tracking-wide uppercase">{t('assistant.title')}</h2>
        <span className="hidden text-xs text-term-muted sm:inline">— {t('assistant.readOnly')}</span>
        {mode === 'min' && busy && <span className="text-xs text-term-prompt">{t('assistant.working')}</span>}
        <span className="flex-1" />
        {mode !== 'min' && (
          <Button size="sm" variant="ghost" className={termButton} onClick={() => setTurns([])} disabled={busy || turns.length === 0} title={t('assistant.clear')}>
            <Eraser aria-hidden="true" />
            <span className="hidden sm:inline">{t('assistant.clear')}</span>
          </Button>
        )}
        <Button size="sm" variant="ghost" className={termButton} onClick={() => setMode(mode === 'min' ? 'normal' : 'min')} aria-label={mode === 'min' ? t('assistant.restore') : t('assistant.minimize')} title={mode === 'min' ? t('assistant.restore') : t('assistant.minimize')}>
          {mode === 'min' ? <ChevronUp aria-hidden="true" /> : <Minus aria-hidden="true" />}
        </Button>
        <Button size="sm" variant="ghost" className={termButton} onClick={() => setMode(mode === 'max' ? 'normal' : 'max')} aria-label={mode === 'max' ? t('assistant.restore') : t('assistant.maximize')} title={mode === 'max' ? t('assistant.restore') : t('assistant.maximize')}>
          {mode === 'max' ? <Minimize2 aria-hidden="true" /> : <Maximize2 aria-hidden="true" />}
        </Button>
        <Button size="sm" variant="ghost" className={termButton} onClick={onClose} aria-label={t('common.close')} title={t('common.close')}>
          <X aria-hidden="true" />
        </Button>
      </div>

      <div className={cn('min-h-0 flex-1 overflow-y-auto px-4 py-3', mode === 'min' && 'hidden')} aria-live="polite">
        {turns.length === 0 && (
          <div className="space-y-3 text-term-muted">
            <p>{t('assistant.intro')}</p>
            <div className="flex flex-wrap gap-2">
              {suggestions.map((s) => (
                <button
                  key={s}
                  type="button"
                  onClick={() => void ask(s)}
                  className="rounded border border-term-border px-2.5 py-1 text-term-fg hover:border-term-prompt hover:text-term-prompt"
                >
                  <span className="text-term-prompt">❯ </span>
                  {s}
                </button>
              ))}
            </div>
            <p className="text-xs">{t('assistant.privacy')}</p>
          </div>
        )}
        <div className="space-y-4">
          {turns.map((turn, i) => (
            <div key={i} className="space-y-1.5">
              <p className="whitespace-pre-wrap">
                <span className="text-term-prompt select-none">❯ </span>
                <span className="font-semibold">{turn.question}</span>
              </p>
              {turn.activity.length > 0 && (
                <ul className="space-y-0.5 text-xs text-term-muted">
                  {turn.activity.map((a, j) => (
                    <li key={j} className="flex items-center gap-1.5">
                      <Wrench className="h-3 w-3 shrink-0" aria-hidden="true" />
                      <span className="truncate">{a}</span>
                    </li>
                  ))}
                </ul>
              )}
              {turn.answer && <AnswerText text={turn.answer} terminal />}
              {turn.error && <p className="text-term-error">✗ {turn.error}</p>}
              {!turn.answer && !turn.error && i === turns.length - 1 && busy && (
                <p className="text-term-muted">
                  {t('assistant.working')}
                  <span className="ml-1 inline-block h-3.5 w-2 animate-pulse bg-term-prompt align-middle motion-reduce:animate-none" aria-hidden="true" />
                </p>
              )}
              {turn.usage && <p className="text-[11px] text-term-muted">{turn.usage}</p>}
            </div>
          ))}
        </div>
        <div ref={bottom} />
      </div>

      <form
        className={cn('flex items-start gap-2 border-t border-term-border px-4 py-2', mode === 'min' && 'hidden')}
        onSubmit={(e) => {
          e.preventDefault()
          void ask(input)
        }}
      >
        <span className="py-1 text-term-prompt select-none" aria-hidden="true">
          ❯
        </span>
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          rows={2}
          maxLength={8000}
          placeholder={t('assistant.placeholder')}
          aria-label={t('assistant.placeholder')}
          className="min-h-0 flex-1 resize-none bg-transparent py-1 text-term-fg caret-term-prompt placeholder:text-term-muted"
          autoFocus
        />
        {busy ? (
          <Button type="button" variant="ghost" className={termButton} onClick={() => abort.current?.abort()} aria-label={t('assistant.stop')}>
            <Square aria-hidden="true" />
          </Button>
        ) : (
          <Button type="submit" variant="ghost" className={termButton} disabled={!input.trim()} aria-label={t('assistant.send')}>
            <Send aria-hidden="true" />
          </Button>
        )}
      </form>
    </section>
  )
}

/** "list_resources pods · shop" from a tool call, for the activity lines. */
function describeTool(name: string, args: Record<string, unknown>): string {
  const parts = ['kind', 'namespace', 'name', 'pod', 'type'].map((k) => args[k]).filter((v) => typeof v === 'string' && v !== '')
  return parts.length > 0 ? `${name} ${parts.join(' · ')}` : name
}
