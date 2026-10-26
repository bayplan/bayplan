import { useTranslation } from 'react-i18next'
import { Search, X } from 'lucide-react'
import { Input } from './ui'

interface SearchInputProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
}

// Search input reused across the resource tabs (pods, namespaces, services,
// nodes). The filtering itself lives in each tab (useMemo over the data
// already loaded by React Query) — this component is only the UI for the
// search term, with no filtering logic of its own.

export function SearchInput({ value, onChange, placeholder }: SearchInputProps) {
  const { t } = useTranslation()
  return (
    <div className="relative w-full max-w-xs">
      <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-fg-muted" />
      <Input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder ?? t('common.searchPlaceholder')}
        className="pr-7 pl-8"
      />
      {value && (
        <button
          type="button"
          onClick={() => onChange('')}
          aria-label={t('common.clearSearch')}
          className="absolute top-1/2 right-2 -translate-y-1/2 text-fg-muted hover:text-fg"
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      )}
    </div>
  )
}
