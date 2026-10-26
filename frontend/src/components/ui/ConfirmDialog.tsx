import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertDialog } from 'radix-ui'
import { Button } from './Button'
import { Input } from './Input'
import { cn } from '../../lib/cn'

export interface ConfirmOptions {
  title: ReactNode
  description?: ReactNode
  /** Confirm button text; defaults to "Confirm". */
  confirmLabel?: ReactNode
  cancelLabel?: ReactNode
  /** Red button for destructive actions. */
  danger?: boolean
  /**
   * Requires typing this text (normally the resource name) to enable the
   * button: replaces the `window.prompt` that compared against the name.
   */
  typeToConfirm?: string
  /** No cancel button: informational notice (replaces `window.alert`). */
  noticeOnly?: boolean
}

type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>

const ConfirmContext = createContext<ConfirmFn | null>(null)

interface Pending {
  id: number
  options: ConfirmOptions
  resolve: (ok: boolean) => void
}

/**
 * Accessible confirmation dialog (Radix AlertDialog: focus trapped, Escape and
 * outside click cancel, `role=alertdialog`). Used as a promise from any
 * handler, just like `window.confirm`, which is what it replaces: the native
 * one cannot be styled, blocks the thread and in some browsers can be muted
 * for the whole tab.
 */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null)
  // The pending promise also lives in a ref so that a second confirm() while
  // one is open can cancel the first: replacing the state alone left its
  // caller awaiting forever (and holding whatever it had in its closure).
  const pendingRef = useRef<Pending | null>(null)
  const nextId = useRef(0)
  const confirm = useCallback<ConfirmFn>((options) => {
    return new Promise<boolean>((resolve) => {
      pendingRef.current?.resolve(false)
      const next = { id: ++nextId.current, options, resolve }
      pendingRef.current = next
      setPending(next)
    })
  }, [])

  function settle(ok: boolean) {
    const current = pendingRef.current
    pendingRef.current = null
    setPending(null)
    current?.resolve(ok)
  }

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {/* Keyed by the request: a replaced dialog starts with an empty "type the name" field. */}
      {pending && <ConfirmDialog key={pending.id} options={pending.options} onSettle={settle} />}
    </ConfirmContext.Provider>
  )
}

export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext)
  if (!ctx) throw new Error('useConfirm must be used inside <ConfirmProvider>')
  return ctx
}

function ConfirmDialog({ options, onSettle }: { options: ConfirmOptions; onSettle: (ok: boolean) => void }) {
  const { t } = useTranslation()
  const [typed, setTyped] = useState('')
  const needsTyping = options.typeToConfirm !== undefined
  const canConfirm = useMemo(() => !needsTyping || typed.trim() === options.typeToConfirm, [needsTyping, typed, options.typeToConfirm])

  return (
    <AlertDialog.Root open onOpenChange={(open) => !open && onSettle(false)}>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="fixed inset-0 z-(--z-modal) bg-black/40" />
        <AlertDialog.Content
          onOpenAutoFocus={(e) => {
            // With text to type the focus goes to the field; otherwise to the
            // cancel button, the safe option if Enter is pressed without looking.
            if (!needsTyping) return
            e.preventDefault()
          }}
          className="fixed top-1/2 left-1/2 z-(--z-modal) w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface p-5 shadow-lg outline-none"
        >
          <AlertDialog.Title className="text-base font-semibold text-fg">{options.title}</AlertDialog.Title>
          {options.description && (
            <AlertDialog.Description className="mt-2 text-sm break-words text-fg-muted">{options.description}</AlertDialog.Description>
          )}
          {needsTyping && (
            <form
              className="mt-3"
              onSubmit={(e) => {
                e.preventDefault()
                if (canConfirm) onSettle(true)
              }}
            >
              <label className="block text-xs text-fg-muted">
                {t('confirm.typeToConfirm', { name: options.typeToConfirm })}
                <Input mono autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} className="mt-1" autoComplete="off" />
              </label>
            </form>
          )}
          <div className={cn('mt-5 flex justify-end gap-2')}>
            {!options.noticeOnly && (
              <AlertDialog.Cancel asChild>
                <Button variant="secondary">{options.cancelLabel ?? t('confirm.cancel')}</Button>
              </AlertDialog.Cancel>
            )}
            <Button
              variant={options.danger ? 'danger' : 'primary'}
              disabled={!canConfirm}
              onClick={() => onSettle(true)}
            >
              {options.confirmLabel ?? (options.noticeOnly ? t('common.close') : t('confirm.confirm'))}
            </Button>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  )
}
