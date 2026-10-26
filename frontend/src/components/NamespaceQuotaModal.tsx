import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery } from '@tanstack/react-query'
import * as resources from '../api/resources'
import { Alert, Button, Checkbox, Field, Input, Modal } from './ui'
import { apiErrorText } from '../lib/errors'

type QuotaField = keyof Omit<resources.NamespaceQuotaPayload, 'createNamespace' | 'dryRun'>

const QUOTA_FIELDS: { key: QuotaField; placeholder: string }[] = [
  { key: 'requestsCpu', placeholder: '4' },
  { key: 'requestsMemory', placeholder: '8Gi' },
  { key: 'limitsCpu', placeholder: '8' },
  { key: 'limitsMemory', placeholder: '16Gi' },
  { key: 'pods', placeholder: '50' },
  { key: 'persistentVolumeClaims', placeholder: '10' },
  { key: 'requestsStorage', placeholder: '100Gi' },
]
const LIMIT_FIELDS: { key: QuotaField; placeholder: string }[] = [
  { key: 'defaultRequestCpu', placeholder: '100m' },
  { key: 'defaultRequestMemory', placeholder: '128Mi' },
  { key: 'defaultLimitCpu', placeholder: '500m' },
  { key: 'defaultLimitMemory', placeholder: '512Mi' },
  { key: 'maxCpu', placeholder: '2' },
  { key: 'maxMemory', placeholder: '4Gi' },
]


/**
 * Namespace quota ("project"): caps (ResourceQuota) and per-container
 * defaults (LimitRange) in a single operation, with a prior dry-run like the
 * YAML editor. Pre-fills the fields with the panel-managed quota if one
 * already exists in that namespace; other quotas in the namespace are not
 * touched.
 */
export function NamespaceQuotaModal({
  clusterId,
  namespaces,
  initialNamespace,
  quotas,
  limitRanges,
  onClose,
  onApplied,
}: {
  clusterId: number
  namespaces: string[]
  initialNamespace?: string
  quotas: resources.ResourceQuotaSummary[]
  limitRanges: resources.LimitRangeSummary[]
  onClose: () => void
  onApplied: () => void
}) {
  const { t } = useTranslation()
  const formId = useId()
  const [namespace, setNamespace] = useState(initialNamespace ?? '')
  const [createNamespace, setCreateNamespace] = useState(false)
  const [values, setValues] = useState<Record<QuotaField, string>>(() => fromExisting(initialNamespace, quotas, limitRanges))
  const [dryRunDone, setDryRunDone] = useState(false)
  const [preview, setPreview] = useState<resources.ApplyResult[] | null>(null)
  const [partial, setPartial] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const nsQuery = useQuery({ queryKey: ['namespaces', clusterId], queryFn: () => resources.listNamespaces(clusterId) })
  const nsExists = (nsQuery.data ?? []).some((n) => n.name === namespace)

  function selectNamespace(ns: string) {
    setNamespace(ns)
    setValues(fromExisting(ns, quotas, limitRanges))
    setDryRunDone(false)
    setPreview(null)
  }
  const set = (k: QuotaField, v: string) => {
    setValues((prev) => ({ ...prev, [k]: v }))
    setDryRunDone(false)
    setPreview(null)
  }

  const mutation = useMutation({
    mutationFn: (dryRun: boolean) => resources.applyNamespaceQuota(clusterId, namespace.trim(), { ...values, createNamespace: createNamespace && !nsExists, dryRun }),
    onSuccess: (res, dryRun) => {
      setError(null)
      if (dryRun) {
        setPreview(res.applied)
        setPartial(Boolean(res.partialDryRun))
        setDryRunDone(true)
      } else {
        onApplied()
        onClose()
      }
    },
    onError: (err) => {
      setError(apiErrorText(err, t('quota.error')))
      setDryRunDone(false)
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    mutation.mutate(!dryRunDone)
  }

  return (
    <Modal
      onClose={onClose}
      size="md"
      title={t('quota.title')}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            {t('clusterAdmin.cancel')}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={mutation.isPending || !namespace}>
            {mutation.isPending ? t('yaml.applying') : dryRunDone ? t('yaml.apply') : t('yaml.dryRun')}
          </Button>
        </>
      }
    >
      <form id={formId} onSubmit={handleSubmit} className="space-y-5">
        <p className="text-xs text-fg-muted">{t('quota.hint')}</p>
        <div>
          <Field label={t('cluster.columns.namespace')}>
            <Input mono list="quota-namespaces" value={namespace} onChange={(e) => selectNamespace(e.target.value)} required pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?" />
          </Field>
          <datalist id="quota-namespaces">
            {namespaces.map((ns) => (
              <option key={ns} value={ns} />
            ))}
          </datalist>
          {namespace && !nsExists && nsQuery.data && (
            <Checkbox className="mt-2" label={t('quota.createNamespace', { namespace })} checked={createNamespace} onChange={(e) => setCreateNamespace(e.target.checked)} />
          )}
        </div>

        <section>
          <h3 className="text-sm font-semibold">{t('quota.limitsTitle')}</h3>
          <p className="mt-0.5 text-xs text-fg-muted">{t('quota.limitsHint')}</p>
          <div className="mt-2 grid grid-cols-2 gap-3 sm:grid-cols-3">
            {QUOTA_FIELDS.map((f) => (
              <Field key={f.key} label={t(`quota.field.${f.key}`)}>
                <Input mono value={values[f.key]} onChange={(e) => set(f.key, e.target.value)} placeholder={f.placeholder} />
              </Field>
            ))}
          </div>
        </section>

        <section>
          <h3 className="text-sm font-semibold">{t('quota.defaultsTitle')}</h3>
          <p className="mt-0.5 text-xs text-fg-muted">{t('quota.defaultsHint')}</p>
          <div className="mt-2 grid grid-cols-2 gap-3 sm:grid-cols-3">
            {LIMIT_FIELDS.map((f) => (
              <Field key={f.key} label={t(`quota.field.${f.key}`)}>
                <Input mono value={values[f.key]} onChange={(e) => set(f.key, e.target.value)} placeholder={f.placeholder} />
              </Field>
            ))}
          </div>
        </section>

        {preview && (
          <div className="rounded-md border border-border bg-neutral-soft px-3 py-2 text-sm">
            <p className="font-medium">{t('yaml.dryRunOk')}</p>
            <ul className="mt-1 space-y-0.5 font-mono text-xs">
              {preview.map((r) => (
                <li key={`${r.kind}/${r.name}`}>
                  {r.kind} {r.namespace ? `${r.namespace}/` : ''}
                  {r.name} · {r.operation}
                </li>
              ))}
            </ul>
            {partial && <p className="mt-1 text-xs text-fg-muted">{t('quota.partialDryRun')}</p>}
          </div>
        )}
        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}

/** Fills the form with the panel's quota/LimitRange if they already exist in the namespace. */

function fromExisting(ns: string | undefined, quotas: resources.ResourceQuotaSummary[], limitRanges: resources.LimitRangeSummary[]): Record<QuotaField, string> {
  const v: Record<QuotaField, string> = {
    requestsCpu: '', requestsMemory: '', limitsCpu: '', limitsMemory: '', pods: '', persistentVolumeClaims: '', requestsStorage: '',
    defaultRequestCpu: '', defaultRequestMemory: '', defaultLimitCpu: '', defaultLimitMemory: '', maxCpu: '', maxMemory: '',
  }
  if (!ns) return v
  const q = quotas.find((x) => x.namespace === ns && x.managedByPanel)
  const map: Record<string, QuotaField> = {
    'requests.cpu': 'requestsCpu', 'requests.memory': 'requestsMemory', 'limits.cpu': 'limitsCpu', 'limits.memory': 'limitsMemory',
    pods: 'pods', persistentvolumeclaims: 'persistentVolumeClaims', 'requests.storage': 'requestsStorage',
  }
  for (const r of q?.resources ?? []) {
    const f = map[r.name]
    if (f) v[f] = r.hard
  }
  const lr = limitRanges.find((x) => x.namespace === ns && x.managedByPanel)
  for (const item of lr?.limits ?? []) {
    if (item.type !== 'Container') continue
    const cap = item.resource === 'cpu' ? 'Cpu' : item.resource === 'memory' ? 'Memory' : ''
    if (!cap) continue
    if (item.defaultRequest) v[`defaultRequest${cap}` as QuotaField] = item.defaultRequest
    if (item.default) v[`defaultLimit${cap}` as QuotaField] = item.default
    if (item.max) v[`max${cap}` as QuotaField] = item.max
  }
  return v
}
