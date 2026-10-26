import type { ComponentProps } from 'react'
import { Loader2 } from 'lucide-react'
import { cn } from '../../lib/cn'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger'
type Size = 'sm' | 'md'

interface ButtonProps extends ComponentProps<'button'> {
  variant?: Variant
  size?: Size
  /** Shows a spinner and disables the button while the action lasts. */
  loading?: boolean
}

const VARIANT_CLASSES: Record<Variant, string> = {
  primary: 'bg-primary text-primary-contrast hover:bg-primary-hover',
  secondary: 'border border-border bg-surface text-fg hover:bg-surface-hover',
  ghost: 'text-fg-muted hover:bg-surface-hover hover:text-fg',
  danger: 'bg-danger text-danger-contrast hover:bg-danger-hover',
}

// Two fixed heights (h-9 / h-8) to align with the inputs; lucide icons inside
// go at 16 px (14 px in sm) through the base rule in index.css.
const SIZE_CLASSES: Record<Size, string> = {
  sm: 'h-8 px-2.5 text-xs [&_svg.lucide]:h-3.5 [&_svg.lucide]:w-3.5',
  md: 'h-9 px-3.5 text-sm',
}

// Consolidated button: until now every page declared its own inline button
// classes, repeated almost character for character. A single component keeps
// the appearance from diverging over time and is the point of change if the
// look-and-feel is adjusted. Accepts `ref` (React 19 passes it as a prop) so
// Radix can use it with `asChild`.
export function Button({ variant = 'secondary', size = 'md', loading, className, type, disabled, children, ...props }: ButtonProps) {
  return (
    <button
      type={type ?? 'button'}
      disabled={disabled || loading}
      className={cn(
        'inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors disabled:pointer-events-none disabled:opacity-60',
        VARIANT_CLASSES[variant],
        SIZE_CLASSES[size],
        className,
      )}
      {...props}
    >
      {loading && <Loader2 className="animate-spin" aria-hidden="true" />}
      {children}
    </button>
  )
}
