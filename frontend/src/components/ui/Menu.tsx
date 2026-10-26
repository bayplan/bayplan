import type { ComponentProps, ReactNode } from 'react'
import { DropdownMenu, Popover as RadixPopover, Tooltip as RadixTooltip } from 'radix-ui'
import { Link } from 'react-router-dom'
import { MoreHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '../../lib/cn'

// Thin wrappers over Radix: positioning, closing on Escape or outside click,
// arrow-key navigation and aria roles come solved. The four hand-made
// implementations there were (language, cluster, namespace, user) repeated the
// same global `pointerdown` and none of them navigated with the keyboard.

const CONTENT_CLASS =
  'z-(--z-popover) min-w-44 overflow-hidden rounded-md border border-border bg-surface-raised py-1 text-fg shadow-lg outline-none data-[state=open]:animate-in'

export const Menu = DropdownMenu.Root
export const MenuTrigger = DropdownMenu.Trigger

export function MenuContent({ className, align = 'end', sideOffset = 4, ...props }: ComponentProps<typeof DropdownMenu.Content>) {
  return (
    <DropdownMenu.Portal>
      <DropdownMenu.Content align={align} sideOffset={sideOffset} className={cn(CONTENT_CLASS, className)} {...props} />
    </DropdownMenu.Portal>
  )
}

const ITEM_CLASS =
  'flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm outline-none select-none data-[highlighted]:bg-surface-hover data-[disabled]:pointer-events-none data-[disabled]:opacity-50 md:py-1.5'

interface MenuItemProps extends Omit<ComponentProps<typeof DropdownMenu.Item>, 'children'> {
  /** Red for destructive actions. */
  danger?: boolean
  /** If given, the item is an SPA link. */
  to?: string
  children: ReactNode
}

export function MenuItem({ className, danger, to, children, ...props }: MenuItemProps) {
  const cls = cn(ITEM_CLASS, danger && 'text-danger data-[highlighted]:bg-danger-soft', className)
  if (to) {
    return (
      <DropdownMenu.Item asChild className={cls} {...props}>
        <Link to={to}>{children}</Link>
      </DropdownMenu.Item>
    )
  }
  return (
    <DropdownMenu.Item className={cls} {...props}>
      {children}
    </DropdownMenu.Item>
  )
}

export function MenuSeparator() {
  return <DropdownMenu.Separator className="my-1 h-px bg-border" />
}

export function MenuLabel({ children }: { children: ReactNode }) {
  return <DropdownMenu.Label className="truncate px-3 py-1.5 text-xs text-fg-muted">{children}</DropdownMenu.Label>
}

interface RowActionsProps {
  label?: string
  children: ReactNode
  className?: string
}

/**
 * "⋮" per-row actions menu. Replaces the inline text links
 * (View logs · View YAML · Terminal · Delete) that took up half the table and
 * did not fit on mobile.
 */
export function RowActions({ label, children, className }: RowActionsProps) {
  const { t } = useTranslation()
  const text = label ?? t('common.actions')
  return (
    <Menu>
      <Tooltip content={text}>
        <MenuTrigger asChild>
          <button
            type="button"
            aria-label={text}
            className={cn(
              // With border and background, like a small secondary button: a bare
              // "⋯" did not read as something clickable.
              'inline-flex h-8 w-8 items-center justify-center rounded-md border border-border bg-surface text-fg-muted shadow-sm hover:border-border-strong hover:bg-surface-hover hover:text-fg data-[state=open]:border-border-strong data-[state=open]:bg-surface-hover data-[state=open]:text-fg',
              className,
            )}
          >
            <MoreHorizontal aria-hidden="true" />
          </button>
        </MenuTrigger>
      </Tooltip>
      <MenuContent>{children}</MenuContent>
    </Menu>
  )
}

// Popover: free-form content (searchable lists, short forms).
export const Popover = RadixPopover.Root
export const PopoverTrigger = RadixPopover.Trigger

export function PopoverContent({ className, align = 'start', sideOffset = 4, ...props }: ComponentProps<typeof RadixPopover.Content>) {
  return (
    <RadixPopover.Portal>
      <RadixPopover.Content
        align={align}
        sideOffset={sideOffset}
        collisionPadding={8}
        className={cn(CONTENT_CLASS, 'w-72 py-0', className)}
        {...props}
      />
    </RadixPopover.Portal>
  )
}

interface TooltipProps {
  content: ReactNode
  children: ReactNode
  side?: 'top' | 'right' | 'bottom' | 'left'
}

export function Tooltip({ content, children, side = 'top' }: TooltipProps) {
  return (
    <RadixTooltip.Root delayDuration={300}>
      <RadixTooltip.Trigger asChild>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content
          side={side}
          sideOffset={6}
          className="z-(--z-toast) max-w-xs rounded-md bg-fg px-2 py-1 text-xs text-canvas shadow-lg"
        >
          {content}
        </RadixTooltip.Content>
      </RadixTooltip.Portal>
    </RadixTooltip.Root>
  )
}

export const TooltipProvider = RadixTooltip.Provider
