import { Logo } from './Logo'
import { APP_NAME } from '../lib/brand'

interface BrandProps {
  size?: 'sm' | 'md'
  className?: string
  /** `brand`: mark in the `brand` grey and normal text (light background); `inherit`: everything in currentColor (topbar, login). */
  tone?: 'brand' | 'inherit'
}

const SIZE_CLASSES = {
  sm: { logo: 'h-6 w-6', text: 'text-base' },
  md: { logo: 'h-9 w-9', text: 'text-2xl' },
}

// Brand lockup reused in the topbar, Login and Setup: 3×3 mark + wordmark.
// Both inherit currentColor from the container (white on the dark topbar,
// `brand` grey on a light background); the only fixed color is the orange of
// the container in the mark. The wordmark goes as text, with the capital B
// from the kit (docs/brand/bayplan-logo.svg).

export function Brand({ size = 'sm', className = '', tone = 'brand' }: BrandProps) {
  const { logo, text } = SIZE_CLASSES[size]

  return (
    <span className={`flex items-center gap-2 ${className}`}>
      <Logo className={`${logo} shrink-0 ${tone === 'brand' ? 'text-brand dark:text-fg' : ''}`} />
      <span className={`truncate font-bold tracking-tight ${text}`}>{APP_NAME}</span>
    </span>
  )
}
