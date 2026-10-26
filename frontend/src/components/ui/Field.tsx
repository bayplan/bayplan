import { cloneElement, isValidElement, useId, type ReactElement, type ReactNode } from 'react'
import { cn } from '../../lib/cn'

interface FieldProps {
  label: ReactNode
  /** Help text under the control. */
  hint?: ReactNode
  /** Validation error: paints the border red and announces it with aria. */
  error?: ReactNode
  required?: boolean
  className?: string
  /** A single control (`Input`, `Select`, `Textarea`…); receives id and aria-*. */
  children: ReactElement<{ id?: string; 'aria-describedby'?: string; 'aria-invalid'?: boolean }>
}

/**
 * Label + control + help/error with the ids linked. Before, every form wrapped
 * the input in a hand-written `<label className="block text-sm">` and the help
 * went in a loose `<p>` without `aria-describedby`.
 */
export function Field({ label, hint, error, required, className, children }: FieldProps) {
  const generatedId = useId()
  // If the control brings its own id (login, setup: browser autofill and tests
  // look it up by that id) it is respected; otherwise one is generated.
  const id = (isValidElement(children) && children.props.id) || generatedId
  const hintId = `${id}-hint`
  const errorId = `${id}-error`
  const described = [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(' ') || undefined
  const control = isValidElement(children)
    ? cloneElement(children, { id, 'aria-describedby': described, 'aria-invalid': error ? true : undefined })
    : children
  return (
    <div className={cn('space-y-1', className)}>
      <label htmlFor={id} className="block text-sm font-medium text-fg">
        {label}
        {required && (
          <span aria-hidden="true" className="ml-0.5 text-danger">
            *
          </span>
        )}
      </label>
      {control}
      {hint && !error && (
        <p id={hintId} className="text-xs text-fg-muted">
          {hint}
        </p>
      )}
      {error && (
        <p id={errorId} className="text-xs text-danger">
          {error}
        </p>
      )}
    </div>
  )
}

/** Group of fields with the same spacing in every form. */
export function FieldGroup({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn('space-y-4', className)}>{children}</div>
}
