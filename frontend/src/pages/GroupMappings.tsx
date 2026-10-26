import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Trash2 } from 'lucide-react'
import * as api from '../api/groupMappings'
import * as clustersApi from '../api/clusters'
import { getAuthStatus } from '../api/auth'
import { Alert, Badge, Button, Card, DataTable, Field, Input, MenuItem, PageHeader, RowActions, Select, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { SearchInput } from '../components/SearchInput'
import { useTableState } from '../hooks/useTableState'
import { useUrlState } from '../hooks/useUrlState'
import { apiErrorText } from '../lib/errors'
import { roleTone } from '../lib/status'
import { ROLES, type Role } from '../api/users'

export function GroupMappings() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const [tableState, setTableState] = useTableState({ sortKey: 'group' })
  const [filters, setFilters] = useUrlState(['q'] as const)

  const mappingsQuery = useQuery({ queryKey: ['group-mappings'], queryFn: api.listGroupMappings })
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: clustersApi.listClusters })
  const statusQuery = useQuery({ queryKey: ['auth-status'], queryFn: getAuthStatus })

  const [adGroupId, setAdGroupId] = useState('')
  const [role, setRole] = useState<Role>('readonly')
  const [scopeType, setScopeType] = useState<'global' | 'cluster'>('global')
  const [scopeRef, setScopeRef] = useState('')
  const [error, setError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: api.createGroupMapping,
    onSuccess: () => {
      setAdGroupId('')
      setScopeRef('')
      setError(null)
      void queryClient.invalidateQueries({ queryKey: ['group-mappings'] })
    },
    onError: (err) => setError(apiErrorText(err, t('groupMappings.createError'))),
  })

  const deleteMutation = useMutation({
    mutationFn: api.deleteGroupMapping,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['group-mappings'] }),
    onError: (err) => setError(apiErrorText(err, t('groupMappings.deleteError'))),
  })

  const mappings = mappingsQuery.data ?? []
  // Key and non-obvious warning: without any row, EVERY SSO login fails with
  // no_role_mapped even if the Azure credentials are correct. Before, this
  // was only written down in deploy/README.md.
  const ssoSinMapeos = statusQuery.data?.ssoEnabled && !mappingsQuery.isPending && mappings.length === 0

  const search = filters.q.trim().toLowerCase()
  const filteredMappings = mappings.filter(
    (m) => !search || m.adGroupId.toLowerCase().includes(search) || m.role.includes(search) || (m.clusterName ?? '').toLowerCase().includes(search),
  )

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate({
      adGroupId,
      role,
      scopeType,
      scopeRef: scopeType === 'cluster' ? scopeRef : undefined,
    })
  }

  const columns: DataTableColumn<api.GroupMapping>[] = [
    {
      key: 'group',
      header: t('groupMappings.adGroupId'),
      cellClassName: 'font-mono text-xs',
      render: (m) => m.adGroupId,
      sortValue: (m) => m.adGroupId,
      primary: true,
    },
    {
      key: 'role',
      header: t('userAdmin.role'),
      render: (m) => <Badge tone={roleTone(m.role)}>{m.role}</Badge>,
      sortValue: (m) => m.role,
      primary: true,
    },
    {
      key: 'scope',
      header: t('userAdmin.scope'),
      render: (m) =>
        m.scopeType === 'global' ? (
          t('userAdmin.scopeGlobal')
        ) : m.clusterName ? (
          m.clusterName
        ) : (
          // scope_ref is TEXT without an FK: it can be left pointing at a deleted
          // cluster, and saying so is better than showing a bare id.
          <span className="text-warning">{t('groupMappings.orphanCluster', { ref: m.scopeRef })}</span>
        ),
      sortValue: (m) => (m.scopeType === 'global' ? '' : (m.clusterName ?? m.scopeRef ?? '')),
    },
  ]

  return (
    <div>
      <PageHeader
        title={t('groupMappings.title')}
        description={t('groupMappings.subtitle')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('groupMappings.title') }]}
      />

      {ssoSinMapeos && (
        <Alert tone="warning" className="mb-4">
          {t('groupMappings.noMappingsWarning')}
        </Alert>
      )}

      {error && (
        <Alert tone="danger" className="mb-4">
          {error}
        </Alert>
      )}

      <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
        <div className="min-w-0">
          <Toolbar>
            <SearchInput value={filters.q} onChange={(v) => setFilters({ q: v })} />
            {search && (
              <Button variant="ghost" size="sm" onClick={() => setFilters({ q: '' })}>
                {t('common.clearFilters')}
              </Button>
            )}
          </Toolbar>
          <DataTable
            columns={columns}
            data={filteredMappings}
            rowKey={(m) => String(m.id)}
            isLoading={mappingsQuery.isPending}
            error={mappingsQuery.isError ? apiErrorText(mappingsQuery.error, t('common.error')) : undefined}
            filtered={search !== ''}
            emptyMessage={t('groupMappings.empty')}
            state={tableState}
            onStateChange={setTableState}
            actions={(m) => (
              <RowActions>
                <MenuItem
                  danger
                  onSelect={async () => {
                    if (!(await confirm({ title: t('groupMappings.confirmDelete', { group: m.adGroupId, role: m.role }), danger: true, confirmLabel: t('groupMappings.delete') }))) return
                    deleteMutation.mutate(m.id)
                  }}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                  {t('groupMappings.delete')}
                </MenuItem>
              </RowActions>
            )}
          />
        </div>

        <Card className="h-fit min-w-0 p-5">
          <h2 className="text-sm font-semibold">{t('groupMappings.addTitle')}</h2>
          <form onSubmit={handleSubmit} className="mt-3 space-y-3">
            <Field label={t('groupMappings.adGroupId')} hint={t('groupMappings.adGroupIdHint')}>
              <Input
                mono
                value={adGroupId}
                onChange={(e) => setAdGroupId(e.target.value)}
                required
                maxLength={256}
                placeholder="00000000-0000-0000-0000-000000000000"
              />
            </Field>

            <Field label={t('userAdmin.role')}>
              <Select value={role} onChange={(e) => setRole(e.target.value as Role)}>
                {ROLES.map((r) => (
                  <option key={r} value={r}>
                    {r} — {t(`userAdmin.roleHint.${r}`)}
                  </option>
                ))}
              </Select>
            </Field>

            <Field label={t('userAdmin.scope')}>
              <Select value={scopeType} onChange={(e) => setScopeType(e.target.value as 'global' | 'cluster')}>
                <option value="global">{t('userAdmin.scopeGlobal')}</option>
                <option value="cluster">{t('userAdmin.scopeCluster')}</option>
              </Select>
            </Field>

            {scopeType === 'cluster' && (
              <Field label={t('groupMappings.cluster')}>
                <Select value={scopeRef} onChange={(e) => setScopeRef(e.target.value)} required>
                  <option value="">—</option>
                  {clustersQuery.data?.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.displayName}
                    </option>
                  ))}
                </Select>
              </Field>
            )}

            <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
              {t('groupMappings.submit')}
            </Button>
          </form>
        </Card>
      </div>
    </div>
  )
}
