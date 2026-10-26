import type { HTMLAttributes } from 'react'

// cardClassName is exported separately for the places that need the same
// appearance on an element that is not a <div> (e.g. the cluster card <Link>
// in Dashboard.tsx, which navigates on click).
export const cardClassName = 'rounded-lg border border-border bg-surface'

export function Card({ className = '', ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={`${cardClassName} ${className}`} {...props} />
}
