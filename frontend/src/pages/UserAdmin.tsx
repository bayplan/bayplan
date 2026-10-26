import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Eye, Power, Trash2, X } from 'lucide-react'
import * as usersApi from '../api/users'
import * as clustersApi from '../api/clusters'
import { useAuth } from '../hooks/useAuth'
import { Alert, Badge, Button, Card, DataTable, Field, Input, MenuItem, PageHeader, RowActions, Select, Toolbar, useConfirm, type DataTableColumn } from '../components/ui'
import { useTableState } from '../hooks/useTableState'
import { roleTone } from '../lib/status'
import { useUrlState } from '../hooks/useUrlState'
import { SearchInput } from '../components/SearchInput'
import { apiErrorText } from '../lib/errors'
import { UserAccessPanel } from '../components/UserAccessPanel'

export function UserAdmin() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const { user: currentUser } = useAuth()

  const usersQuery = useQuery({ queryKey: ['users'], queryFn: usersApi.listUsers })
  const [tableState, setTableState] = useTableState({ sortKey: 'username' })
  const clustersQuery = useQuery({ queryKey: ['clusters'], queryFn: clustersApi.listClusters })

  const [detailOf, setDetailOf] = useState<usersApi.UserWithRoles | null>(null)
  // The search lives in the URL like the rest of the application's filters.
  const [filters, setFilters] = useUrlState(['q'] as const)
  const [selectedUserId, setSelectedUserId] = useState<number | ''>('')
  const [role, setRole] = useState<usersApi.Role>('readonly')
  const [scopeType, setScopeType] = useState<'global' | 'cluster'>('global')
  const [scopeRef, setScopeRef] = useState('')
  const [error, setError] = useState<string | null>(null)

  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [createError, setCreateError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: usersApi.createUser,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users'] })
      setNewUsername('')
      setNewPassword('')
      setCreateError(null)
    },
    onError: (err) => setCreateError(apiErrorText(err, t('userAdmin.createError'))),
  })

  function handleCreateSubmit(e: FormEvent) {
    e.preventDefault()
    setCreateError(null)
    createMutation.mutate({ username: newUsername, password: newPassword })
  }

  const grantMutation = useMutation({
    mutationFn: ({ userId, payload }: { userId: number; payload: usersApi.GrantRolePayload }) =>
      usersApi.grantRole(userId, payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users'] })
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('userAdmin.error'))),
  })

  const revokeMutation = useMutation({
    mutationFn: ({ userId, assignmentId }: { userId: number; assignmentId: number }) =>
      usersApi.revokeRole(userId, assignmentId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users'] })
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('userAdmin.error'))),
  })

  const deleteMutation = useMutation({
    mutationFn: (userId: number) => usersApi.deleteUser(userId),
    onSuccess: (_data, userId) => {
      queryClient.invalidateQueries({ queryKey: ['users'] })
      setError(null)
      // The grant-role form keeps the selected id in state: if it pointed at
      // the deleted user, "Grant" would fail against an id that no longer
      // exists.
      setSelectedUserId((prev) => (prev === userId ? '' : prev))
    },
    onError: (err) => setError(apiErrorText(err, t('userAdmin.actionError'))),
  })

  const disableMutation = useMutation({
    mutationFn: ({ userId, disabled }: { userId: number; disabled: boolean }) =>
      usersApi.setUserDisabled(userId, disabled),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users'] })
      setError(null)
    },
    onError: (err) => setError(apiErrorText(err, t('userAdmin.actionError'))),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (!selectedUserId) return
    grantMutation.mutate({
      userId: selectedUserId,
      payload: { role, scopeType, scopeRef: scopeType === 'cluster' ? scopeRef : undefined },
    })
  }

  // Client-side filtering: the user list of an internal panel fits entirely in
  // memory, there is nothing to gain by paginating it on the server. It also searches
  // by source so the SSO users can be isolated at a glance.
  const search = filters.q.trim().toLowerCase()
  const filteredUsers = (usersQuery.data ?? []).filter(
    (u) => !search || u.username.toLowerCase().includes(search) || u.source.toLowerCase().includes(search),
  )

  const columns: DataTableColumn<usersApi.UserWithRoles>[] = [
    {
      key: 'username',
      header: t('userAdmin.username'),
      cellClassName: 'font-medium align-top',
      sortValue: (u) => u.username,
      primary: true,
      // The detail (effective permissions, which is what one really wants to know
      // about a user and cannot be deduced from the loose "Roles" assignments) is
      // opened from the row menu, like the rest of the actions.
      render: (u) => (
        <>
          {u.username}
          {u.disabled && <span className="ml-1 text-xs font-normal text-danger">({t('userAdmin.disabled')})</span>}
        </>
      ),
    },
    {
      key: 'source',
      header: t('userAdmin.source'),
      cellClassName: 'text-fg-muted align-top',
      render: (u) => u.source,
      sortValue: (u) => u.source,
    },
    {
      key: 'roles',
      header: t('userAdmin.roles'),
      cellClassName: 'align-top',
      primary: true,
      render: (u) =>
        u.roles.length === 0 ? (
          <span className="text-fg-muted">{t('userAdmin.noRoles')}</span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {u.roles.map((ra) => (
              <Badge key={ra.id} tone={roleTone(ra.role)} className="pr-1">
                {ra.role}
                {ra.scopeType !== 'global' && ` · ${ra.clusterName ?? ra.scopeRef}`}
                {/* Revoking a permission is destructive and until now it was done with
                    a single click and without asking, next to actions that did
                    confirm. */}
                <button
                  type="button"
                  onClick={async () => {
                    const alcance = ra.scopeType === 'global' ? t('userAdmin.scopeGlobal') : (ra.clusterName ?? ra.scopeRef ?? '')
                    if (!(await confirm({ title: t('userAdmin.confirmRevoke', { role: ra.role, scope: alcance, username: u.username }), danger: true, confirmLabel: t('userAdmin.revoke') }))) return
                    revokeMutation.mutate({ userId: u.id, assignmentId: ra.id })
                  }}
                  title={t('userAdmin.revoke')}
                  className="ml-0.5 rounded-full hover:bg-black/10"
                >
                  <X className="h-3 w-3" aria-hidden="true" />
                </button>
              </Badge>
            ))}
          </div>
        ),
    },
  ]

  return (
    <div>
      <PageHeader
        title={t('userAdmin.title')}
        breadcrumb={[{ label: t('nav.dashboard'), to: '/' }, { label: t('nav.sectionAdmin') }, { label: t('userAdmin.title') }]}
      />
      {error && (
        <Alert tone="danger" className="mb-4">
          {error}
        </Alert>
      )}
      <div className="grid gap-8 lg:grid-cols-[3fr_2fr]">
        <div className="min-w-0">
          <Toolbar>
            <SearchInput
              value={filters.q}
              onChange={(v) => setFilters({ q: v })}
              placeholder={t('userAdmin.searchPlaceholder')}
            />
            {search && (
              <Button variant="ghost" size="sm" onClick={() => setFilters({ q: '' })}>
                {t('common.clearFilters')}
              </Button>
            )}
          </Toolbar>
          <DataTable
            columns={columns}
            data={filteredUsers}
            rowKey={(u) => String(u.id)}
            isLoading={usersQuery.isPending}
            error={usersQuery.isError ? apiErrorText(usersQuery.error, t('common.error')) : undefined}
            filtered={search !== ''}
            state={tableState}
            onStateChange={setTableState}
            actions={(u) => {
              // Actions on oneself are hidden because the backend rejects them
              // with 409 (self-lockout): there is no point offering them. Ids are
              // compared, not names, since /api/auth/me exposes the id.
              const esUnoMismo = currentUser?.id === u.id
              return (
                <RowActions>
                  <MenuItem onSelect={() => setDetailOf(u)}>
                    <Eye className="h-4 w-4" aria-hidden="true" />
                    {t('userAdmin.access.title')}
                  </MenuItem>
                  {!esUnoMismo && (
                    <MenuItem disabled={disableMutation.isPending} onSelect={() => disableMutation.mutate({ userId: u.id, disabled: !u.disabled })}>
                      <Power className="h-4 w-4" aria-hidden="true" />
                      {u.disabled ? t('userAdmin.enable') : t('userAdmin.disable')}
                    </MenuItem>
                  )}
                  {!esUnoMismo && u.source === 'local' && (
                    <MenuItem
                      danger
                      disabled={deleteMutation.isPending}
                      onSelect={async () => {
                        if (!(await confirm({ title: t('userAdmin.delete'), description: t('userAdmin.confirmDelete', { username: u.username }), danger: true, confirmLabel: t('userAdmin.delete') }))) return
                        deleteMutation.mutate(u.id)
                      }}
                    >
                      <Trash2 className="h-4 w-4" aria-hidden="true" />
                      {t('userAdmin.delete')}
                    </MenuItem>
                  )}
                </RowActions>
              )
            }}
          />
        </div>

        <div className="flex min-w-0 flex-col gap-6">
        <Card className="p-5">
          <h2 className="text-lg font-semibold">{t('userAdmin.createTitle')}</h2>
          <form className="mt-4 space-y-4" onSubmit={handleCreateSubmit}>
            <Field label={t('userAdmin.newUsername')}>
              <Input
                value={newUsername}
                onChange={(e) => setNewUsername(e.target.value)}
                required
                autoComplete="off"
              />
            </Field>
            <Field label={t('userAdmin.newPassword')}>
              <Input
                type="password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                required
                minLength={12}
                autoComplete="new-password"
              />
            </Field>

            {createError && <Alert tone="danger">{createError}</Alert>}

            <Button type="submit" variant="primary" disabled={createMutation.isPending} className="w-full">
              {t('userAdmin.createSubmit')}
            </Button>
          </form>
        </Card>

        <Card className="p-5">
          <h2 className="text-lg font-semibold">{t('userAdmin.grantTitle')}</h2>
          <form className="mt-4 space-y-4" onSubmit={handleSubmit}>
            <Field label={t('userAdmin.user')}>
              <Select
                value={selectedUserId}
                onChange={(e) => setSelectedUserId(e.target.value ? Number(e.target.value) : '')}
                required
              >
                <option value="">—</option>
                {usersQuery.data?.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.username}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label={t('userAdmin.role')}>
              <Select value={role} onChange={(e) => setRole(e.target.value as usersApi.Role)}>
                {usersApi.ROLES.map((r) => (
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
              <Field label={t('cluster.title')}>
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

            <Button type="submit" variant="primary" disabled={grantMutation.isPending} className="w-full">
              {t('userAdmin.grantSubmit')}
            </Button>
          </form>
        </Card>
        </div>
      </div>

      {detailOf && <UserAccessPanel user={detailOf} onClose={() => setDetailOf(null)} />}
    </div>
  )
}
