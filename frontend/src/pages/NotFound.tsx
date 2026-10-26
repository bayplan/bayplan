import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card } from '../components/ui'

// There was no catch-all route before: a mistyped URL rendered the layout with
// blank content, indistinguishable from a loading failure.
export function NotFound() {
  const { t } = useTranslation()

  return (
    <Card className="mx-auto max-w-md p-8 text-center">
      <p className="text-sm font-semibold tracking-wide text-fg-muted">404</p>
      <h1 className="mt-1 text-xl font-semibold tracking-tight">{t('notFound.title')}</h1>
      <p className="mt-2 text-sm text-fg-muted">{t('notFound.description')}</p>
      <Link to="/" className="mt-5 inline-block text-sm text-link hover:underline">
        {t('notFound.back')}
      </Link>
    </Card>
  )
}
