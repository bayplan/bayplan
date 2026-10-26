import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import * as tokensApi from '../api/tokens'
import { SearchInput } from '../components/SearchInput'
import { Alert, Badge, Button, Card, EmptyState, PageHeader, SegmentedControl, Toolbar, type BadgeTone } from '../components/ui'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'

const METHOD_TONE: Record<string, BadgeTone> = { get: 'success', post: 'warning', put: 'warning', patch: 'warning', delete: 'danger' }

/**
 * API browser built from /api/openapi.json, with no external
 * dependencies (Swagger UI would go to a CDN and the installation may have no
 * internet access). It shows routes, method, parameters and permissions; tokens are
 * created in the profile.
 */
export function ApiDocs() {
  const { t } = useTranslation()
  const specQuery = useQuery({ queryKey: ['openapi'], queryFn: tokensApi.getOpenAPI, staleTime: Infinity })
  // The search lives in the URL like in the rest of the lists; the group tab
  // is ephemeral interaction state.
  const [filters, setFilters] = useUrlState(['q'] as const)
  const query = filters.q
  const [tag, setTag] = useState<string>('')

  const operations = useMemo(() => {
    const spec = specQuery.data
    if (!spec) return []
    const out: { path: string; method: string; op: tokensApi.OpenAPIOperation }[] = []
    for (const [path, methods] of Object.entries(spec.paths)) {
      for (const [method, op] of Object.entries(methods)) out.push({ path, method, op })
    }
    return out
  }, [specQuery.data])
  const tags = specQuery.data?.tags?.map((x) => x.name) ?? []
  const q = query.trim().toLowerCase()
  const filtered = useMemo(
    () => operations.filter((o) => (!tag || o.op.tags?.includes(tag)) && (!q || o.path.toLowerCase().includes(q) || (o.op.summary ?? '').toLowerCase().includes(q))),
    [operations, tag, q],
  )
  const grouped = useMemo(() => {
    const m = new Map<string, typeof filtered>()
    for (const o of filtered) {
      const k = o.op.tags?.[0] ?? '—'
      if (!m.has(k)) m.set(k, [])
      m.get(k)!.push(o)
    }
    return Array.from(m.entries())
  }, [filtered])

  return (
    <div>
      <PageHeader title={t('apiDocs.title')} description={specQuery.data?.info.description} breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('apiDocs.title') }]} />
      <Card className="mb-4 p-4 text-sm">
        <p>
          {t('apiDocs.authHint')}{' '}
          <Link to="/profile" className="underline">
            {t('profile.title')}
          </Link>
          .
        </p>
        <pre className="mt-2 max-w-full overflow-x-auto rounded-md bg-neutral-soft px-3 py-2 font-mono text-xs whitespace-pre-wrap break-all">{`curl -H "Authorization: Bearer bpl_…" ${window.location.origin}/api/clusters\ncurl -H "Authorization: Bearer bpl_…" "${window.location.origin}/api/clusters/1/pods?namespace=app"\ncurl -X POST -H "Authorization: Bearer bpl_…" -H "Content-Type: application/json" \\\n  -d '{"replicas":3}' ${window.location.origin}/api/clusters/1/deployments/app/api/scale`}</pre>
        <p className="mt-2 text-xs text-fg-muted">
          {t('apiDocs.specHint')} <code>/api/openapi.json</code>
        </p>
      </Card>
      <Toolbar>
        <SearchInput value={query} onChange={(v) => setFilters({ q: v })} placeholder={t('common.searchPlaceholder')} />
        <SegmentedControl
          size="sm"
          label={t('apiDocs.all')}
          value={tag}
          onChange={setTag}
          options={[{ value: '', label: t('apiDocs.all') }, ...tags.map((x) => ({ value: x, label: x }))]}
        />
        {(q || tag) && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setFilters({ q: '' })
              setTag('')
            }}
          >
            {t('common.clearFilters')}
          </Button>
        )}
      </Toolbar>
      {specQuery.isPending && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
      {specQuery.isError && <Alert tone="danger">{apiErrorText(specQuery.error, t('common.error'))}</Alert>}
      {specQuery.data && grouped.length === 0 && <EmptyState title={t('common.noResults')} />}
      <div className="space-y-6">
        {grouped.map(([group, ops]) => (
          <section key={group}>
            <h2 className="mb-2 text-sm font-semibold">{group}</h2>
            <ul className="divide-y divide-border rounded-md border border-border">
              {ops.map(({ path, method, op }) => (
                <li key={`${method} ${path}`} className="px-3 py-2 text-sm">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge tone={METHOD_TONE[method] ?? 'neutral'}>{method.toUpperCase()}</Badge>
                    <code className="font-mono text-xs break-all">{path}</code>
                    <span className="text-fg-muted">{op.summary}</span>
                  </div>
                  {(op.description || op.parameters?.some((p) => p.in === 'query')) && (
                    <p className="mt-1 text-xs text-fg-muted">
                      {op.description}
                      {op.parameters
                        ?.filter((p) => p.in === 'query')
                        .map((p) => (
                          <span key={p.name}>
                            {' '}
                            · <code>?{p.name}</code> {p.description}
                          </span>
                        ))}
                    </p>
                  )}
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </div>
  )
}
