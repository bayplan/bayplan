import { Logo } from './Logo'
import { Wordmark } from './Wordmark'
import { APP_NAME } from '../lib/brand'

interface BrandProps {
  size?: 'sm' | 'md'
  className?: string
  /** `brand`: mark in the `brand` grey and normal text (light background); `inherit`: everything in currentColor (topbar, login). */
  tone?: 'brand' | 'inherit'
}

const SIZE_CLASSES = {
  sm: { logo: 'h-6 w-6', text: 'h-6' },
  md: { logo: 'h-9 w-9', text: 'h-9' },
}

// Brand lockup reused in the topbar, Login and Setup: 3×3 mark + wordmark.
// Both inherit currentColor from the container (white on the dark topbar,
// `brand` grey on a light background); the only fixed color is the orange of
// the container in the mark. The wordmark is the kit's own drawing
// (docs/brand/bayplan-logo.svg), not text, so at the same height as the mark
// it spans exactly the mark's squares, as in the kit and on the website.

export function Brand({ size = 'sm', className = '', tone = 'brand' }: BrandProps) {
  const { logo, text } = SIZE_CLASSES[size]

  return (
    <span className={`flex items-center gap-1.5 ${className}`}>
      <Logo className={`${logo} shrink-0 ${tone === 'brand' ? 'text-brand dark:text-fg' : ''}`} />
      <Wordmark className={`${text} w-auto min-w-0`} label={APP_NAME} />
    </span>
  )
}
