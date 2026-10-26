import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { listAudit } from '../api/audit'
import { getUserAccess, type UserWithRoles } from '../api/users'
import { Alert, Badge, DataTable, Modal, type DataTableColumn } from '../components/ui'
import type { ClusterAccess } from '../api/users'
import { roleTone } from '../lib/status'
import { apiErrorText } from '../lib/errors'

const RECENT_ACTIONS = 10

/**
 * User detail: what they can really do and what they have done lately.
 *
 * The matrix is per user, not per role: what someone can really do is what an
 * admin needs to check. Our model —two roles, two scopes— is small enough to
 * resolve it exactly.
 */
export function UserAccessPanel({ user, onClose }: { user: UserWithRoles; onClose: () => void }) {
  const { t, i18n } = useTranslation()

  const accessQuery = useQuery({
    queryKey: ['user-access', user.id],
    queryFn: () => getUserAccess(user.id),
  })

  // Reuses the audit endpoint instead of inventing one: it is already
  // restricted to global admin and already knows how to filter by actor.
  const auditQuery = useQuery({
    queryKey: ['user-audit', user.username],
    queryFn: () => listAudit({ actor: user.username }, undefined),
  })
  const recent = auditQuery.data?.items.slice(0, RECENT_ACTIONS) ?? []

  const columns: DataTableColumn<ClusterAccess>[] = [
    { key: 'cluster', header: t('userAdmin.access.cluster'), cellClassName: 'font-mono', render: (c) => c.cluster, primary: true },
    {
      key: 'role',
      header: t('userAdmin.access.effectiveRole'),
      primary: true,
      render: (c) =>
        c.role ? (
          <Badge tone={roleTone(c.role)}>{c.role}</Badge>
        ) : (
          <span className="text-fg-muted">{t('userAdmin.access.noAccess')}</span>
        ),
    },
    {
      key: 'source',
      header: t('userAdmin.access.source'),
      cellClassName: 'text-xs text-fg-muted',
      // The source is the actionable datum: revoking the cluster assignment
      // removes nothing if the permission came from the global role.

      render: (c) => (c.source ? t(`userAdmin.access.from.${c.source}`) : '—'),
    },
  ]

  return (
    <Modal
      onClose={onClose}
      title={<span className="font-mono">{user.username}</span>}
      description={
        <span className="flex flex-wrap items-center gap-1.5">
          <Badge tone="neutral">{user.source}</Badge>
          {user.disabled && <Badge tone="danger">{t('userAdmin.disabled')}</Badge>}
          {accessQuery.data?.globalRole ? (
            <span>
              {t('userAdmin.access.globalRole')}: <strong>{accessQuery.data.globalRole}</strong>
            </span>
          ) : (
            <span>{t('userAdmin.access.noGlobalRole')}</span>
          )}
        </span>
      }
      bodyClassName="space-y-5"
    >
      <section>
        <h3 className="mb-2 text-sm font-semibold">{t('userAdmin.access.title')}</h3>
        <p className="mb-2 text-xs text-fg-muted">{t('userAdmin.access.hint')}</p>
        <DataTable
          columns={columns}
          data={accessQuery.data?.clusters ?? []}
          rowKey={(c) => String(c.clusterId)}
          isLoading={accessQuery.isPending}
          error={accessQuery.isError ? apiErrorText(accessQuery.error, t('common.error')) : undefined}
          emptyMessage={t('clusterAdmin.empty')}
        />
      </section>

      <section>
        <div className="mb-2 flex items-baseline justify-between gap-2">
          <h3 className="text-sm font-semibold">{t('userAdmin.access.recentTitle')}</h3>
          <Link
            to={`/admin/audit?actor=${encodeURIComponent(user.username)}`}
            className="text-xs text-link hover:underline"
          >
            {t('userAdmin.access.seeAllAudit')}
          </Link>
        </div>
        {auditQuery.isPending && <p className="text-sm text-fg-muted">{t('common.loading')}</p>}
        {auditQuery.isError && <Alert tone="danger">{apiErrorText(auditQuery.error, t('common.error'))}</Alert>}
        {!auditQuery.isPending && !auditQuery.isError && recent.length === 0 && (
          <p className="text-sm text-fg-muted">{t('audit.empty')}</p>
        )}
        <ul className="divide-y divide-border">
          {recent.map((e) => (
            <li key={e.id} className="flex flex-wrap items-baseline gap-2 py-1.5 text-sm">
              <span className="font-mono text-xs">{e.action}</span>
              <Badge tone={e.result === 'success' ? 'success' : e.result === 'denied' ? 'danger' : 'warning'}>
                {t(`audit.result.${e.result}`, { defaultValue: e.result })}
              </Badge>
              {e.cluster && <span className="text-xs text-fg-muted">{e.cluster}</span>}
              <span className="ml-auto text-xs text-fg-muted">
                {new Date(e.ts).toLocaleString(i18n.resolvedLanguage)}
              </span>
            </li>
          ))}
        </ul>
      </section>
    </Modal>
  )
}
