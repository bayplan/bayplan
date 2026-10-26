import { useEffect, useId, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { X } from 'lucide-react'
import { cn } from '../../lib/cn'

const FOCUSABLE =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])'

type Size = 'sm' | 'md' | 'lg' | 'xl'

// Two drawer widths (md for forms, lg for detail) and two for the centered
// modal. There used to be five (`max-w-xl/2xl/3xl/4xl` and `max-w-sm/md/lg`).
const DRAWER_WIDTH: Record<Size, string> = {
  sm: 'max-w-md',
  md: 'max-w-xl',
  lg: 'max-w-3xl',
  xl: 'max-w-5xl',
}
const CENTER_WIDTH: Record<Size, string> = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
  xl: 'max-w-4xl',
}

interface ModalProps {
  onClose: () => void
  /**
   * `drawer` sticks the panel to the right edge at full height, `center`
   * centers it, `top` anchors it at the top (command palette: vertically
   * centered it would sit far from where the eye expects a search box).
   */
  variant?: 'drawer' | 'center' | 'top'
  size?: Size
  /** Standard header with a close button; without a title the body goes bare. */
  title?: ReactNode
  description?: ReactNode
  /** Fixed footer (buttons). */
  footer?: ReactNode
  /** Panel classes (width, height…). The overlay is provided by Modal itself. */
  className?: string
  /** Classes for the scrollable body (padding by default). */
  bodyClassName?: string
  children: ReactNode
}

/**
 * Accessible overlay for panels and dialogs. Before, every panel mounted its own
 * `fixed inset-0 bg-black/40` and none closed on Escape, trapped focus or
 * returned focus to the element that opened it: with the keyboard you could tab
 * "behind" the open panel into the page underneath.
 *
 * With `title` the panel comes with a fixed header, scrollable body and fixed
 * footer: the same header was copied 14 times and the centered modals had no
 * `max-h`, so on short screens the content was cut off with no way to scroll it.
 *
 * Note: `TerminalPanel` deliberately does NOT use this component. Escape is a
 * legitimate key **inside** a terminal (vim, less, any TUI), so closing the
 * panel with Escape there would break the user's session mid-command; and the
 * focus has to stay with xterm, not an external trap.
 */
export function Modal({ onClose, variant = 'drawer', size = 'lg', title, description, footer, className = '', bodyClassName, children }: ModalProps) {
  const { t } = useTranslation()
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const descId = useId()

  // No scrolling behind while the dialog is open: with a drawer open the mouse
  // wheel over the backdrop scrolled the table underneath.
  useEffect(() => {
    const main = document.querySelector('main')
    const prevOverflow = main?.style.overflow ?? ''
    if (main) main.style.overflow = 'hidden'
    return () => {
      if (main) main.style.overflow = prevOverflow
    }
  }, [])

  // Escape calls the latest onClose without it being an effect dependency:
  // callers pass inline arrows, so with [onClose] every parent re-render (a
  // keystroke in a field of the modal, a 15 s poll) ran the effect again,
  // moved the focus to the first element and back, and typing lost focus.
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  })

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    // Focus the first useful element of the body (not the header's close
    // button), or the panel itself: if it stays on the button that opened the
    // modal, the first Tab press goes behind. An `autoFocus` field inside
    // already took the focus, and it is respected.
    const panel = panelRef.current
    if (panel && !panel.contains(document.activeElement)) {
      const body = panel.querySelector<HTMLElement>('[data-modal-body]') ?? panel
      const first = Array.from(body.querySelectorAll<HTMLElement>(FOCUSABLE)).find((el) => !el.hasAttribute('data-modal-close'))
      ;(first ?? panel).focus()
    }

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onCloseRef.current()
        return
      }
      if (event.key !== 'Tab' || !panelRef.current) return
      const focusables = Array.from(panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE))
      if (focusables.length === 0) return
      const edge = event.shiftKey ? focusables[0] : focusables[focusables.length - 1]
      // Only intervene at the edges: inside the panel, Tab is still the
      // browser's Tab (respects DOM order and tabindex).
      if (document.activeElement === edge) {
        event.preventDefault()
        ;(event.shiftKey ? focusables[focusables.length - 1] : focusables[0]).focus()
      }
    }

    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('keydown', onKeyDown)
      previous?.focus()
    }
  }, [])

  const framed = title !== undefined

  return (
    <div
      className={cn(
        'fixed inset-0 z-(--z-modal) flex bg-black/40',
        variant === 'drawer' && 'items-stretch justify-end',
        variant === 'top' && 'items-start justify-center p-4 pt-[15vh]',
        variant === 'center' && 'items-center justify-center p-4',
      )}
      // Clicking the backdrop closes, but only if the click is ON the backdrop:
      // without the check, dragging a text selection inside the panel and
      // releasing the mouse outside would close the dialog.
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={framed ? titleId : undefined}
        aria-describedby={framed && description ? descId : undefined}
        tabIndex={-1}
        className={cn(
          'flex flex-col bg-surface shadow-lg outline-none',
          variant === 'drawer'
            ? cn('h-full w-full', DRAWER_WIDTH[size])
            : cn('max-h-full w-full rounded-lg', variant === 'top' ? 'max-w-xl' : CENTER_WIDTH[size]),
          className,
        )}
      >
        {framed && (
          <div className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-5 py-3.5">
            <div className="min-w-0">
              <h2 id={titleId} className="truncate text-base font-semibold text-fg">
                {title}
              </h2>
              {description && (
                <p id={descId} className="mt-0.5 text-xs text-fg-muted">
                  {description}
                </p>
              )}
            </div>
            <button
              type="button"
              data-modal-close
              onClick={onClose}
              aria-label={t('common.close')}
              className="-mr-1.5 shrink-0 rounded-md p-1.5 text-fg-muted hover:bg-surface-hover hover:text-fg"
            >
              <X aria-hidden="true" />
            </button>
          </div>
        )}
        {framed ? (
          <div data-modal-body className={cn('min-h-0 flex-1 overflow-y-auto px-5 py-4', bodyClassName)}>
            {children}
          </div>
        ) : (
          children
        )}
        {framed && footer && <div className="flex shrink-0 flex-wrap justify-end gap-2 border-t border-border px-5 py-3.5">{footer}</div>}
      </div>
    </div>
  )
}
