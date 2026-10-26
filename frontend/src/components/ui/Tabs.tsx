import type { ReactNode } from 'react'

export interface TabItem<K extends string> {
  key: K
  label: ReactNode
}

interface TabsProps<K extends string> {
  items: TabItem<K>[]
  active: K
  onChange: (key: K) => void
}

export function Tabs<K extends string>({ items, active, onChange }: TabsProps<K>) {
  return (
    <div className="flex gap-1 overflow-x-auto border-b border-border [scrollbar-width:none]" role="tablist">
      {items.map((item) => (
        <button
          key={item.key}
          type="button"
          role="tab"
          aria-selected={active === item.key}
          onClick={() => onChange(item.key)}
          className={`shrink-0 whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
            active === item.key
              ? 'border-primary text-primary'
              : 'border-transparent text-fg-muted hover:text-fg'
          }`}
        >
          {item.label}
        </button>
      ))}
    </div>
  )
}
