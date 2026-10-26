import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, CheckCircle2, Info, OctagonAlert, X } from 'lucide-react'
import { cn } from '../../lib/cn'

export type AlertTone = 'info' | 'success' | 'warning' | 'danger'

interface AlertProps {
  tone?: AlertTone
  title?: ReactNode
  children?: ReactNode
  /** If given, the notice carries a close button. */
  onClose?: () => void
  className?: string
}

const TONE: Record<AlertTone, { box: string; icon: typeof Info }> = {
  info: { box: 'border-info/30 bg-info-soft text-fg', icon: Info },
  success: { box: 'border-success/30 bg-success-soft text-fg', icon: CheckCircle2 },
  warning: { box: 'border-warning/30 bg-warning-soft text-fg', icon: AlertTriangle },
  danger: { box: 'border-danger/30 bg-danger-soft text-fg', icon: OctagonAlert },
}

const ICON_TONE: Record<AlertTone, string> = {
  info: 'text-info',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
}

/**
 * Inline notice. Replaces the 12 variants of `<p className="text-danger">`
 * and the Docker `ActionError` banner copied inline in 4 places. The text goes
 * with `break-words`: API server errors bring long paths and JSON.
 */
export function Alert({ tone = 'info', title, children, onClose, className }: AlertProps) {
  const { t } = useTranslation()
  const { box, icon: Icon } = TONE[tone]
  return (
    <div role={tone === 'danger' || tone === 'warning' ? 'alert' : 'status'} className={cn('flex gap-2.5 rounded-md border px-3 py-2.5 text-sm', box, className)}>
      <Icon className={cn('mt-0.5 shrink-0', ICON_TONE[tone])} aria-hidden="true" />
      <div className="min-w-0 flex-1 break-words">
        {title && <p className="font-medium">{title}</p>}
        {children && <div className={cn(title && 'mt-0.5 text-fg-muted')}>{children}</div>}
      </div>
      {onClose && (
        <button
          type="button"
          onClick={onClose}
          aria-label={t('common.close')}
          className="-mr-1 -mt-0.5 shrink-0 rounded-md p-1 text-fg-muted hover:bg-black/5 hover:text-fg"
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      )}
    </div>
  )
}
