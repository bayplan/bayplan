import type { InputHTMLAttributes, ReactNode } from 'react'
import { Switch as RadixSwitch } from 'radix-ui'
import { cn } from '../../lib/cn'

interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  label: ReactNode
  hint?: ReactNode
}

// Native checkbox with `accent-color`: inherits the primary color, works with
// keyboard and screen readers without code, and the indeterminate state and
// forms (`required`, `form`) come for free.
export function Checkbox({ label, hint, className, ...props }: CheckboxProps) {
  return (
    <label className={cn('flex items-start gap-2 text-sm', props.disabled && 'opacity-60', className)}>
      <input type="checkbox" className="mt-0.5 h-4 w-4 shrink-0 cursor-pointer accent-primary" {...props} />
      <span>
        <span className="text-fg">{label}</span>
        {hint && <span className="block text-xs text-fg-muted">{hint}</span>}
      </span>
    </label>
  )
}

interface SwitchProps {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  label: ReactNode
  hint?: ReactNode
  disabled?: boolean
  className?: string
}

/** Toggle for settings that apply instantly (enable, activate). */
export function Switch({ checked, onCheckedChange, label, hint, disabled, className }: SwitchProps) {
  return (
    <label className={cn('flex items-center gap-3 text-sm', disabled && 'opacity-60', className)}>
      <RadixSwitch.Root
        checked={checked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
        className="relative h-5 w-9 shrink-0 cursor-pointer rounded-full bg-border-strong transition-colors data-[state=checked]:bg-primary"
      >
        <RadixSwitch.Thumb className="block h-4 w-4 translate-x-0.5 rounded-full bg-white shadow-sm transition-transform data-[state=checked]:translate-x-4" />
      </RadixSwitch.Root>
      <span>
        <span className="text-fg">{label}</span>
        {hint && <span className="block text-xs text-fg-muted">{hint}</span>}
      </span>
    </label>
  )
}
