import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Copy, RefreshCw, Sparkles } from 'lucide-react'
import { explain, getAIStatus, type ExplainTarget } from '../api/ai'
import { useCopy } from '../hooks/useCopy'
import { apiErrorText } from '../lib/errors'
import { cn } from '../lib/cn'
import { Alert, Button, Modal, Spinner } from './ui'

const ROLE_RANK: Record<string, number> = { readonly: 1, editor: 2, admin: 3 }

/** Whether "Explain" is offered on a cluster where the user has `role`. */
export function useExplainAvailable(role: string | undefined) {
  const status = useQuery({ queryKey: ['ai-status'], queryFn: getAIStatus, staleTime: 60_000, retry: false })
  const ready = status.data?.ready === true
  return ready && (ROLE_RANK[role ?? ''] ?? 0) >= (ROLE_RANK[status.data?.minRole ?? 'editor'] ?? 2)
}

interface ExplainButtonProps extends ExplainTarget {
  clusterId: number
  /** The user's role on this cluster (from /api/clusters). */
  role: string | undefined
  size?: 'sm' | 'md'
  variant?: 'secondary' | 'ghost'
}

/**
 * "Explain with AI" for one object. Hidden when AI is not configured or the
 * user's role on the cluster is below the configured minimum; the server
 * checks both again. The call is only made on click, never prefetched: each
 * one costs money and is audited.
 */
export function ExplainButton({ clusterId, role, kind, namespace, name, size = 'sm', variant = 'secondary' }: ExplainButtonProps) {
  const { t } = useTranslation()
  const available = useExplainAvailable(role)
  const [open, setOpen] = useState(false)
  if (!available) return null
  return (
    <>
      <Button size={size} variant={variant} onClick={() => setOpen(true)} title={t('explain.button')}>
        <Sparkles aria-hidden="true" />
        {t('explain.button')}
      </Button>
      {open && <ExplainModal clusterId={clusterId} target={{ kind, namespace, name }} onClose={() => setOpen(false)} />}
    </>
  )
}

function ExplainModal({ clusterId, target, onClose }: { clusterId: number; target: ExplainTarget; onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const lang = i18n.resolvedLanguage ?? 'en'
  const { copy, copied } = useCopy()
  // A mutation and not a query: it must not refetch on focus or remount, each
  // call is billed. It fires once on open (the ref survives StrictMode's
  // double effect run) and again only on "Regenerate".
  const run = useMutation({ mutationFn: () => explain(clusterId, target, lang) })
  const started = useRef(false)
  useEffect(() => {
    if (started.current) return
    started.current = true
    run.mutate()
  }, [run])
  const result = run.data

  return (
    <Modal
      onClose={onClose}
      variant="center"
      size="lg"
      title={t('explain.title', { name: target.name })}
      description={<span className="font-mono text-xs">{target.namespace ? `${target.kind} · ${target.namespace}/${target.name}` : `${target.kind} · ${target.name}`}</span>}
      footer={
        <>
          {result && (
            <Button size="sm" variant="ghost" onClick={() => void copy(result.text)}>
              <Copy aria-hidden="true" />
              {copied ? t('common.copied') : t('common.copy')}
            </Button>
          )}
          <Button size="sm" onClick={() => run.mutate()} disabled={run.isPending}>
            <RefreshCw aria-hidden="true" />
            {t('explain.regenerate')}
          </Button>
          <Button size="sm" variant="primary" onClick={onClose}>
            {t('common.close')}
          </Button>
        </>
      }
    >
      {run.isPending && (
        <div className="flex items-center gap-3 py-6 text-sm text-fg-muted">
          <Spinner />
          {t('explain.thinking')}
        </div>
      )}
      {run.isError && <Alert tone="danger">{apiErrorText(run.error, t('explain.error'))}</Alert>}
      {result && !run.isPending && (
        <div className="space-y-4">
          <AnswerText text={result.text} />
          <details className="rounded-md border border-border px-3 py-2 text-xs text-fg-muted">
            <summary className="cursor-pointer">{t('explain.sent', { provider: result.provider, model: result.model })}</summary>
            <ul className="mt-2 list-disc space-y-1 pl-5">
              {result.sent.map((s) => (
                <li key={s.title}>
                  <span className="text-fg">{s.title}</span>: {s.summary}
                </li>
              ))}
            </ul>
            <p className="mt-2">{t('explain.redactionNote')}</p>
          </details>
          <p className="text-xs text-fg-muted">
            {t('explain.disclaimer')} · {t('explain.usage', { tokens: result.inputTokens + result.outputTokens, seconds: Math.round(result.durationMs / 1000), remaining: result.remainingToday, count: result.remainingToday })}
          </p>
        </div>
      )}
    </Modal>
  )
}

/**
 * Renders the plain-text answer the prompt asks for: lines, "-" bullets and
 * `code`. No Markdown library and no HTML from the model: everything is text
 * nodes, so a model echoing a log line with markup cannot inject anything.
 */
export function AnswerText({ text, terminal = false }: { text: string; terminal?: boolean }) {
  const blocks: ReactNode[] = []
  let bullets: string[] = []
  const flush = () => {
    if (bullets.length > 0) {
      blocks.push(
        <ul key={blocks.length} className="list-disc space-y-1 pl-5">
          {bullets.map((b, i) => (
            <li key={i}>{inlineCode(b, terminal)}</li>
          ))}
        </ul>,
      )
      bullets = []
    }
  }
  for (const raw of text.split('\n')) {
    const line = raw.trimEnd()
    const bullet = /^\s*[-*•]\s+(.*)$/.exec(line)
    if (bullet) {
      bullets.push(bullet[1])
      continue
    }
    flush()
    if (line.trim() === '') continue
    // A short line ending in ":" or without final punctuation reads as a heading.
    const heading = line.length < 70 && !/[.;,]$/.test(line)
    blocks.push(
      <p key={blocks.length} className={heading ? cn('font-semibold', terminal ? 'text-term-fg' : 'text-fg') : undefined}>
        {inlineCode(line.replace(/^#+\s*/, '').replace(/\*\*/g, ''), terminal)}
      </p>,
    )
  }
  flush()
  return <div className={cn('space-y-2 leading-relaxed', terminal ? 'text-[13px]' : 'text-sm')}>{blocks}</div>
}

function inlineCode(line: string, terminal = false): ReactNode {
  return line.split(/(`[^`]+`)/).map((part, i) =>
    part.startsWith('`') && part.endsWith('`') && part.length > 2 ? (
      <code key={i} className={cn('rounded px-1 py-0.5 font-mono text-xs', terminal ? 'bg-term-surface text-term-prompt' : 'bg-neutral-soft')}>
        {part.slice(1, -1)}
      </code>
    ) : (
      <Fragment key={i}>{part}</Fragment>
    ),
  )
}
