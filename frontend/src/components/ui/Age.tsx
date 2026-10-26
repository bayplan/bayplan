import { useTranslation } from 'react-i18next'
import { ageParts } from '../../lib/status'
import { formatDateTime } from '../../lib/dates'

/**
 * Age of a resource in compact form, with the full date in the tooltip: a
 * table cell needs "3 d", but the exact value is still useful when diagnosing
 * something.
 *
 * It does not refresh by itself: cluster listings refetch every 7 s and that
 * keeps the value up to date without mounting a timer per row.
 */
export function Age({ createdAt }: { createdAt: string }) {
  const { t, i18n } = useTranslation()
  const parts = ageParts(createdAt)
  if (!parts) return null

  return (
    <span title={formatDateTime(createdAt, i18n.resolvedLanguage)}>
      {t(`common.age.${parts.unit}`, { value: parts.value })}
    </span>
  )
}
