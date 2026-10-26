import type { BadgeTone } from './Badge'

// The classes are written out in full and not composed with a template: Tailwind v4
// resolves arbitrary values (`bg-success`…) by scanning the source text,
// so a class built at runtime never gets generated and the dot would come
// out without color.
const DOT_CLASSES: Record<BadgeTone, string> = {
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  neutral: 'bg-fg-muted',
}

/** Compact status indicator, for when a whole badge is too much. */
export function StatusDot({ tone, title }: { tone: BadgeTone; title?: string }) {
  return <span title={title} className={`h-2.5 w-2.5 shrink-0 rounded-full ${DOT_CLASSES[tone]}`} />
}
