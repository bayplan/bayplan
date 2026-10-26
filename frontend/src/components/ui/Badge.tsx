import type { ReactNode } from 'react'

export type BadgeTone = 'success' | 'warning' | 'danger' | 'neutral'

interface BadgeProps {
  tone?: BadgeTone
  children: ReactNode
  className?: string
}

const TONE_CLASSES: Record<BadgeTone, string> = {
  success: 'bg-success-soft text-success',
  warning: 'bg-warning-soft text-warning',
  danger: 'bg-danger-soft text-danger',
  neutral: 'bg-neutral-soft text-fg-muted',
}

// Real status pill (background + text), instead of the previous "loose
// colored text" pattern the resource tables used — more legible in dense
// tables and consistent with the visual vocabulary of Prism/vSphere consoles
// (green=OK, amber=warning, red=error, grey=neutral/stopped).
export function Badge({ tone = 'neutral', children, className = '' }: BadgeProps) {
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${TONE_CLASSES[tone]} ${className}`}
    >
      {children}
    </span>
  )
}
