import type { InputHTMLAttributes, SelectHTMLAttributes, TextareaHTMLAttributes } from 'react'
import { cn } from '../../lib/cn'

// A single class string for every text control. It used to be copied 25 times
// in 20 files plus 17 nearly identical local `INPUT_CLASS` constants (some with
// `bg-transparent`, others with `bg-surface`, some with `mt-1` and others
// without), so two adjacent forms did not look the same.
const inputClassName =
  'h-9 w-full rounded-md border border-border bg-surface px-3 text-sm text-fg placeholder:text-fg-subtle transition-colors focus:border-primary disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-danger'

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  /** Monospaced font: tokens, URLs, resource names. */
  mono?: boolean
  invalid?: boolean
}

export function Input({ className, mono, invalid, ...props }: InputProps) {
  return (
    <input
      aria-invalid={invalid || undefined}
      className={cn(inputClassName, mono && 'font-mono text-xs', className)}
      {...props}
    />
  )
}

interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  mono?: boolean
  invalid?: boolean
}

export function Textarea({ className, mono, invalid, ...props }: TextareaProps) {
  return (
    <textarea
      aria-invalid={invalid || undefined}
      className={cn(inputClassName, 'h-auto min-h-24 py-2', mono && 'font-mono text-xs', className)}
      {...props}
    />
  )
}

interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean
}

// Styled native select and not Radix's: the native one works with keyboard,
// screen readers and on mobile (system pickers) without custom code, and no
// list in the UI needs a search box or icons inside the options.
export function Select({ className, invalid, ...props }: SelectProps) {
  return (
    <select
      aria-invalid={invalid || undefined}
      className={cn(inputClassName, 'cursor-pointer appearance-none bg-[url("data:image/svg+xml;charset=utf-8,%3Csvg xmlns=%27http://www.w3.org/2000/svg%27 width=%2716%27 height=%2716%27 viewBox=%270 0 24 24%27 fill=%27none%27 stroke=%27%235e5270%27 stroke-width=%272%27 stroke-linecap=%27round%27 stroke-linejoin=%27round%27%3E%3Cpath d=%27m6 9 6 6 6-6%27/%3E%3C/svg%3E")] bg-[length:16px] bg-[right_0.5rem_center] bg-no-repeat pr-8', className)}
      {...props}
    />
  )
}
