import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import * as metricsApi from '../api/metrics'
import { Alert, Badge, Button, Checkbox, Modal } from './ui'
import { apiErrorText } from '../lib/errors'


/**
 * Install metrics-server from the panel: mandatory dry-run and then apply,
 * like the YAML editor. If the cluster's RBAC does not allow it (scoped mode
 * without reapplying), the API server error is shown as is and the usual
 * route remains: download the manifest and apply it with kubectl.

 */
export function MetricsServerModal({ clusterId, onClose }: { clusterId: number; onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [insecure, setInsecure] = useState(false)
  const [preview, setPreview] = useState<metricsApi.MetricsServerInstallResult | null>(null)
  const [done, setDone] = useState<metricsApi.MetricsServerInstallResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: (dryRun: boolean) => metricsApi.installMetricsServer(clusterId, { dryRun, kubeletInsecureTLS: insecure }),
    onSuccess: (res, dryRun) => {
      setError(null)
      if (dryRun) {
        setPreview(res)
      } else {
        setDone(res)
        void queryClient.invalidateQueries({ queryKey: ['metrics', clusterId] })
      }
    },
    onError: (err) => setError(apiErrorText(err, t('metricsServer.error'))),
  })

  return (
    <Modal
      onClose={onClose}
      variant="center"
      size="lg"
      title={t('metricsServer.title')}
      footer={
        <>
          <a href={metricsApi.metricsServerManifestUrl(clusterId, insecure)} download className="mr-auto inline-flex items-center gap-1 self-center text-sm text-link hover:underline">
            <Download className="h-3.5 w-3.5" aria-hidden="true" />
            {t('metricsServer.download')}
          </a>
          <Button onClick={onClose}>{t('common.close')}</Button>
          {!done && (
            <Button variant="primary" disabled={mutation.isPending} onClick={() => mutation.mutate(!preview)}>
              {mutation.isPending ? t('yaml.applying') : preview ? t('metricsServer.install') : t('yaml.dryRun')}
            </Button>
          )}
        </>
      }
    >
      <p className="text-sm text-fg-muted">{t('metricsServer.hint')}</p>
      <Checkbox
        className="mt-4"
        label={t('metricsServer.insecure')}
        hint={t('metricsServer.insecureHint')}
        checked={insecure}
        onChange={(e) => {
          setInsecure(e.target.checked)
          setPreview(null)
        }}
      />

      {preview && !done && (
        <div className="mt-4 rounded-md border border-border bg-neutral-soft px-3 py-2 text-sm">
          <p className="font-medium">{t('yaml.dryRunOk')}</p>
          <ul className="mt-1 space-y-0.5 font-mono text-xs">
            {preview.applied.map((r) => (
              <li key={`${r.kind}/${r.namespace ?? ''}/${r.name}`}>
                <Badge tone={r.operation === 'created' ? 'success' : 'neutral'}>{t(`yaml.op.${r.operation}`)}</Badge> {r.kind} {r.namespace ? `${r.namespace}/` : ''}
                {r.name}
              </li>
            ))}
          </ul>
          {preview.partialDryRun && <p className="mt-1 text-xs text-fg-muted">{t('metricsServer.partialDryRun')}</p>}
        </div>
      )}
      {done && (
        <Alert tone="success" className="mt-4">
          {t('metricsServer.installed', { version: done.version })}
        </Alert>
      )}
      {error && (
        <Alert tone="danger" className="mt-4" title={error}>
          {t('metricsServer.fallback')}
        </Alert>
      )}
    </Modal>
  )
}
