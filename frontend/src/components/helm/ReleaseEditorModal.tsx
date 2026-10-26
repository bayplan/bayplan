import { useEffect, useId, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { CodeEditor } from '../CodeEditor'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Play } from 'lucide-react'
import * as helmApi from '../../api/helm'
import * as resources from '../../api/resources'
import { Alert, Badge, Button, Checkbox, Field, Input, Modal, Select } from '../ui'
import { apiErrorText } from '../../lib/errors'

export interface ReleaseEditorPreset {
  repoId?: number
  chart?: string
  version?: string
}

interface Props {
  clusterId: number
  mode: 'install' | 'upgrade'
  preset?: ReleaseEditorPreset
  /** On upgrade: the current release (name, namespace, chart, values). */
  existing?: helmApi.ReleaseDetail
  defaultNamespace?: string
  onClose: () => void
  onDone: (result: helmApi.ReleaseResult) => void
}


/**
 * Install or upgrade a release. Same contract as the YAML editor: nothing is
 * applied without a prior dry-run of the current content, and the dry-run is
 * done by the API server (`helm install --dry-run=server`), so it validates
 * the cluster's schema, admission and RBAC, not just the template.
 */
export function ReleaseEditorModal({ clusterId, mode, preset, existing, defaultNamespace, onClose, onDone }: Props) {
  const { t } = useTranslation()
  const formId = useId()
  const [repoId, setRepoId] = useState<number | undefined>(preset?.repoId)
  const [chartQuery, setChartQuery] = useState(preset?.chart ?? existing?.chart ?? '')
  const [chart, setChart] = useState(preset?.chart ?? existing?.chart ?? '')
  const [version, setVersion] = useState(preset?.version ?? '')
  const [name, setName] = useState(existing?.name ?? preset?.chart ?? '')
  const [namespace, setNamespace] = useState(existing?.namespace ?? defaultNamespace ?? 'default')
  const [createNamespace, setCreateNamespace] = useState(false)
  const [values, setValues] = useState(existing?.values ?? '')
  const [valuesTouched, setValuesTouched] = useState(mode === 'upgrade')
  const [error, setError] = useState<string | null>(null)
  const [preview, setPreview] = useState<helmApi.ReleaseResult | null>(null)
  const [previewKey, setPreviewKey] = useState('')

  const reposQuery = useQuery({ queryKey: ['helm-repos'], queryFn: helmApi.listHelmRepos })
  const repos = reposQuery.data ?? []
  const repo = repos.find((r) => r.id === repoId)
  const firstRepoId = reposQuery.data?.[0]?.id
  useEffect(() => {
    if (repoId === undefined && firstRepoId !== undefined) setRepoId(firstRepoId)
  }, [firstRepoId, repoId])

  const namespacesQuery = useQuery({ queryKey: ['namespaces', clusterId], queryFn: () => resources.listNamespaces(clusterId) })

  // Charts of the repo matching the text; in OCI the text IS the chart name.
  const chartsQuery = useQuery({
    queryKey: ['helm-charts', repoId, repo?.type === 'oci' ? chartQuery : ''],
    queryFn: () => helmApi.searchCharts(repoId!, '', repo?.type === 'oci' ? { chart: chartQuery } : {}),
    enabled: repoId !== undefined && (repo?.type !== 'oci' || chartQuery.length > 0),
    staleTime: 5 * 60 * 1000,
  })
  const matches = useMemo(() => {
    const all = chartsQuery.data ?? []
    const q = chartQuery.trim().toLowerCase()
    if (!q) return all.slice(0, 30)
    return all.filter((c) => c.name.toLowerCase().includes(q)).slice(0, 30)
  }, [chartsQuery.data, chartQuery])
  const selected = (chartsQuery.data ?? []).find((c) => c.name === chart)

  // When choosing a chart without a version, the latest.
  useEffect(() => {
    if (selected && !version) setVersion(selected.versions[0] ?? '')
  }, [selected, version])

  // Default values of the chosen chart (install only and as long as the user
  // has not touched the textarea).
  const infoQuery = useQuery({
    queryKey: ['helm-chart-info', repoId, chart, version],
    queryFn: () => helmApi.getChartInfo(repoId!, chart, version),
    enabled: repoId !== undefined && !!chart && !!version,
    staleTime: 10 * 60 * 1000,
  })
  useEffect(() => {
    if (!valuesTouched && infoQuery.data) setValues(infoQuery.data.defaultValues)
  }, [infoQuery.data, valuesTouched])

  const input = (dryRun: boolean): helmApi.ReleaseInput => ({
    repoId: repoId!,
    chart,
    version,
    name,
    namespace,
    values,
    createNamespace,
    dryRun,
  })
  const key = JSON.stringify(input(true))

  const run = (dryRun: boolean) =>
    mode === 'install'
      ? helmApi.installRelease(clusterId, input(dryRun))
      : helmApi.upgradeRelease(clusterId, existing!.namespace, existing!.name, input(dryRun))

  const dryRunMutation = useMutation({
    mutationFn: () => run(true),
    onSuccess: (res) => {
      setPreview(res)
      setPreviewKey(key)
      setError(null)
    },
    onError: (err) => {
      setPreview(null)
      setError(apiErrorText(err, t('helm.error')))
    },
  })
  const applyMutation = useMutation({
    mutationFn: () => run(false),
    onSuccess: onDone,
    onError: (err) => setError(apiErrorText(err, t('helm.error'))),
  })

  const previewValid = preview !== null && previewKey === key
  const ready = repoId !== undefined && chart && version && name && namespace

  function handleDryRun(e: FormEvent) {
    e.preventDefault()
    if (!ready) return
    dryRunMutation.mutate()
  }

  return (
    <Modal
      onClose={onClose}
      size="xl"
      title={mode === 'install' ? t('helm.installTitle') : t('helm.upgradeTitle', { name: existing?.name })}
      footer={
        <>
          {(error || (previewValid && preview)) && (
            <div className="w-full space-y-2">
              {error && <Alert tone="danger">{error}</Alert>}
              {previewValid && preview && (
                <details className="rounded-md bg-neutral-soft px-3 py-2 text-xs">
                  <summary className="cursor-pointer font-medium">
                    {t('helm.dryRunOk', { chart: preview.release.chart, version: preview.release.chartVersion })}
                    <Badge tone="neutral" className="ml-2">
                      {t('helm.manifestLines', { count: (preview.manifest ?? '').split('\n').length })}
                    </Badge>
                  </summary>
                  <pre className="mt-2 max-h-64 overflow-auto font-mono whitespace-pre">{preview.manifest}</pre>
                </details>
              )}
            </div>
          )}
          <Button variant="secondary" onClick={onClose} disabled={applyMutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} disabled={!ready || dryRunMutation.isPending}>
            {dryRunMutation.isPending ? t('yaml.validating') : t('yaml.dryRun')}
          </Button>
          <Button
            variant="primary"
            onClick={() => applyMutation.mutate()}
            disabled={!previewValid || applyMutation.isPending}
            title={previewValid ? undefined : t('yaml.dryRunFirst')}
          >
            <Play className="h-3.5 w-3.5" aria-hidden="true" />
            {applyMutation.isPending ? t('helm.working') : mode === 'install' ? t('helm.install') : t('helm.upgrade')}
          </Button>
        </>
      }
    >
      {/* min-h-full: the values editor stretches to the body's height even if
          the fields column is shorter. */}
      <form id={formId} onSubmit={handleDryRun} className="grid min-h-full gap-4 lg:grid-cols-[1fr_1.4fr]">
        <div className="space-y-3">
          <Field label={t('helm.repo')}>
            <Select value={repoId ?? ''} onChange={(e) => { setRepoId(Number(e.target.value)); setChart(''); setVersion(''); setValuesTouched(mode === 'upgrade') }}>
              {repos.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name} ({r.type})
                </option>
              ))}
            </Select>
          </Field>
          {repos.length === 0 && !reposQuery.isPending && <Alert tone="warning">{t('helm.noRepos')}</Alert>}

          <Field label={t('helm.chart')}>
            <Input
              mono
              value={chartQuery}
              onChange={(e) => {
                setChartQuery(e.target.value)
                setChart(repo?.type === 'oci' ? e.target.value : '')
                setVersion('')
              }}
              placeholder={repo?.type === 'oci' ? t('helm.ociChartPlaceholder') : t('helm.searchChart')}

            />
          </Field>
          {repo?.type !== 'oci' && chartQuery && chart !== chartQuery && (
            <ul className="max-h-48 overflow-y-auto rounded-md border border-border text-sm">
              {chartsQuery.isPending && <li className="px-3 py-2 text-fg-muted">{t('common.loading')}</li>}
              {matches.map((c) => (
                <li key={c.name}>
                  <button
                    type="button"
                    onClick={() => {
                      setChart(c.name)
                      setChartQuery(c.name)
                      setVersion(c.versions[0] ?? '')
                      if (mode === 'install' && !name) setName(c.name)
                      setValuesTouched(mode === 'upgrade')
                    }}
                    className="flex w-full items-baseline justify-between gap-2 px-3 py-1.5 text-left hover:bg-surface-hover"
                  >
                    <span className="font-mono">{c.name}</span>
                    <span className="truncate text-xs text-fg-muted">{c.description}</span>
                  </button>
                </li>
              ))}
              {!chartsQuery.isPending && matches.length === 0 && (
                <li className="px-3 py-2 text-fg-muted">{t('common.noResults')}</li>
              )}
            </ul>
          )}

          <Field label={t('helm.version')}>
            {selected ? (
              <Select value={version} onChange={(e) => { setVersion(e.target.value); setValuesTouched(mode === 'upgrade') }}>
                {selected.versions.map((v) => (
                  <option key={v} value={v}>
                    {v}
                  </option>
                ))}
              </Select>
            ) : (
              <Input mono value={version} onChange={(e) => setVersion(e.target.value)} placeholder="1.2.3" />
            )}
          </Field>

          <Field label={t('helm.releaseName')}>
            <Input
              mono
              value={name}
              onChange={(e) => setName(e.target.value)}
              disabled={mode === 'upgrade'}
              required
              pattern="[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?"
            />
          </Field>
          <Field label={t('cluster.columns.namespace')}>
            <Input
              mono
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              disabled={mode === 'upgrade'}
              required
              list="helm-namespaces"
            />
          </Field>
          <datalist id="helm-namespaces">
            {namespacesQuery.data?.map((n) => (
              <option key={n.name} value={n.name} />
            ))}
          </datalist>
          {mode === 'install' && (
            <Checkbox label={t('helm.createNamespace')} checked={createNamespace} onChange={(e) => setCreateNamespace(e.target.checked)} />
          )}

          {infoQuery.data && (
            <div className="rounded-md bg-neutral-soft px-3 py-2 text-xs">
              <div className="font-medium">
                {infoQuery.data.name} {infoQuery.data.version}
                {infoQuery.data.appVersion && <span className="text-fg-muted"> · app {infoQuery.data.appVersion}</span>}
              </div>
              {infoQuery.data.description && <p className="mt-1 text-fg-muted">{infoQuery.data.description}</p>}
            </div>
          )}
        </div>

        <div className="flex min-h-64 flex-col text-sm">
          <div className="flex items-baseline justify-between">
            <span className="font-medium text-fg">{t('helm.values')}</span>
            {infoQuery.data && valuesTouched && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setValues(infoQuery.data!.defaultValues)
                  setValuesTouched(false)
                }}
              >
                {t('helm.resetValues')}
              </Button>
            )}
          </div>
          <div className="mt-1 flex min-h-0 flex-1 flex-col overflow-hidden rounded-md border border-border">
            <CodeEditor
              value={values}
              onChange={(v) => {
                setValues(v)
                setValuesTouched(true)
              }}
              placeholder={infoQuery.isPending && chart && version ? t('common.loading') : '# values.yaml'}
            />
          </div>
        </div>
      </form>
    </Modal>
  )
}
