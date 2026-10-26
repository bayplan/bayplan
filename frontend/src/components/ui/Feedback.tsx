import type { HTMLAttributes, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Loader2, type LucideIcon } from 'lucide-react'
import { cn } from '../../lib/cn'

/** Inline loading indicator (buttons, headers). */
export function Spinner({ className, label }: { className?: string; label?: string }) {
  const { t } = useTranslation()
  return (
    <span role="status" aria-label={label ?? t('common.loading')} className={cn('inline-flex items-center gap-2 text-sm text-fg-muted', className)}>
      <Loader2 className="animate-spin" aria-hidden="true" />
      {label}
    </span>
  )
}

/** Grey block that holds the place of what is loading. */
export function Skeleton({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div aria-hidden="true" className={cn('animate-pulse rounded-md bg-neutral-soft', className)} {...props} />
}

interface EmptyStateProps {
  icon?: LucideIcon
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
  className?: string
}

/**
 * Single empty state: icon, title, explanation and, when it applies, the action
 * that resolves it. Before, every list wrote its own centered paragraph.
 */
export function EmptyState({ icon: Icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div className={cn('flex flex-col items-center justify-center gap-2 px-6 py-12 text-center', className)}>
      {Icon && <Icon className="h-8 w-8 text-fg-subtle" aria-hidden="true" />}
      <p className="text-sm font-medium text-fg">{title}</p>
      {description && <p className="max-w-md text-sm text-fg-muted">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

interface ToolbarProps {
  /** Search box, filters… (left). */
  children?: ReactNode
  /** List actions (right). */
  actions?: ReactNode
  className?: string
}

/**
 * Standard toolbar under the header: filters on the left and list actions on
 * the right, which move to a second row on narrow screens. Every list uses
 * it; whatever does not apply is omitted.
 */
export function Toolbar({ children, actions, className }: ToolbarProps) {
  return (
    <div className={cn('mb-3 flex flex-wrap items-center gap-2', className)}>
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">{children}</div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

export interface DescriptionItem {
  label: ReactNode
  value: ReactNode
  /** Monospaced value (ids, images, paths). */
  mono?: boolean
}

interface DescriptionListProps {
  items: DescriptionItem[]
  /** Columns on wide screens; always one on mobile. */
  columns?: 1 | 2 | 3
  className?: string
}

/** Key → value list for the detail pages. */
export function DescriptionList({ items, columns = 2, className }: DescriptionListProps) {
  return (
    <dl
      className={cn(
        'grid gap-x-6 gap-y-3 text-sm',
        columns === 2 && 'sm:grid-cols-2',
        columns === 3 && 'sm:grid-cols-2 lg:grid-cols-3',
        className,
      )}
    >
      {items.map((item, i) => (
        <div key={i} className="min-w-0">
          <dt className="text-xs text-fg-muted">{item.label}</dt>
          <dd className={cn('mt-0.5 break-words text-fg', item.mono && 'font-mono text-xs')}>{item.value}</dd>
        </div>
      ))}
    </dl>
  )
}

export interface SegmentOption<K extends string> {
  value: K
  label: ReactNode
}

interface SegmentedControlProps<K extends string> {
  options: SegmentOption<K>[]
  value: K
  onChange: (value: K) => void
  label?: string
  size?: 'sm' | 'md'
  className?: string
}

/** Group of mutually exclusive buttons (channel type, date range…). */
export function SegmentedControl<K extends string>({ options, value, onChange, label, size = 'md', className }: SegmentedControlProps<K>) {
  return (
    <div role="radiogroup" aria-label={label} className={cn('inline-flex flex-wrap rounded-md bg-neutral-soft p-0.5', className)}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded-[5px] font-medium transition-colors',
            size === 'sm' ? 'px-2 py-1 text-xs' : 'px-3 py-1.5 text-sm',
            value === o.value ? 'bg-surface text-fg shadow-sm' : 'text-fg-muted hover:text-fg',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
