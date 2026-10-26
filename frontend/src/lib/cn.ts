import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

// Class composition for the ui/ primitives: clsx accepts conditionals and
// twMerge resolves conflicts (the component's `px-3` against the caller's
// `px-0`: the last one wins), which with string concatenation depended on the
// stylesheet order and not on the prop order.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
