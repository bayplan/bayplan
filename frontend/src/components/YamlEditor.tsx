import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, Maximize2, Minimize2, Pencil, Play, RotateCcw } from 'lucide-react'
import * as resources from '../api/resources'
import { Alert, Badge, Button, Modal } from './ui'
import { CodeEditor } from './CodeEditor'
import { apiErrorText } from '../lib/errors'
import { useCopy } from '../hooks/useCopy'

interface YamlEditorProps {
  clusterId: number
  /** Without `kind`/`name` it is a new resource: empty editor with a template. */
  kind?: resources.YamlKind
  namespace?: string
  name?: string
  /** Default namespace for objects without one (the one selected in the topbar). */
  defaultNamespace?: string
  /** Whether the user can apply (editor or admin role on the cluster). */
  canWrite: boolean
  onClose: () => void
  /** After a successful real apply, to invalidate listings. */
  onApplied?: (result: resources.ApplyResponse) => void
  /**
   * No overlay: the editor lives inside the page (YAML tab of the detail),
   * with its toolbar and its validate/apply footer. `onClose` is ignored.
   */
  inline?: boolean
}

const NEW_TEMPLATE = `apiVersion: v1
kind: ConfigMap
metadata:
  name: example
data:
  key: value
`


/**
 * View and edit YAML. The save flow is deliberately two-step, like
 * `kubectl apply --dry-run=server` before the apply: the API server validates
 * schema, admission webhooks and RBAC without persisting anything, and the
 * user sees which objects would be created or changed before confirming.
 * Apply is only enabled after a clean dry-run of the CURRENT text; any
 * keystroke invalidates it.
 *
 * The editor is CodeMirror (components/CodeEditor.tsx), not Monaco:
 * highlighting, line numbers and folding for a fraction of the weight.
 */
export function YamlEditor({ clusterId, kind, namespace, name, defaultNamespace, canWrite, onClose, onApplied, inline }: YamlEditorProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const isNew = !kind || !name
  const [editing, setEditing] = useState(isNew)
  const [text, setText] = useState(isNew ? NEW_TEMPLATE : '')
  const { copy, copied } = useCopy()
  // The drawer at full width: a node's YAML or a Deployment with many
  // annotations does not fit in 48rem and long lines get wrapped.
  const [fullscreen, setFullscreen] = useState(false)
  const [preview, setPreview] = useState<resources.ApplyResponse | null>(null)
  const [previewFor, setPreviewFor] = useState<string>('')
  const [error, setError] = useState<string | null>(null)
  const [applied, setApplied] = useState<resources.ApplyResponse | null>(null)

  const query = useQuery({
    queryKey: ['resource-yaml', clusterId, kind, namespace, name],
    queryFn: () => resources.getResourceYAML(clusterId, kind!, namespace ?? '', name!),
    enabled: !isNew,
  })

  // The editor text starts from the server's YAML; it is reloaded if the
  // displayed resource changes, not while editing.
  useEffect(() => {
    if (!isNew && query.data !== undefined && !editing) setText(query.data)
  }, [query.data, isNew, editing])

  const dryRunMutation = useMutation({
    mutationFn: () => resources.applyYAML(clusterId, text, defaultNamespace ?? namespace ?? '', true),
    onSuccess: (res) => {
      setPreview(res)
      setPreviewFor(text)
      setError(null)
    },
    onError: (err) => {
      setPreview(null)
      setError(apiErrorText(err, t('yaml.applyError')))
    },
  })

  const applyMutation = useMutation({
    mutationFn: () => resources.applyYAML(clusterId, text, defaultNamespace ?? namespace ?? '', false),
    onSuccess: (res) => {
      setApplied(res)
      setPreview(null)
      setError(null)
      setEditing(false)
      void queryClient.invalidateQueries({ queryKey: ['resource-yaml', clusterId] })
      onApplied?.(res)
    },
    onError: (err) => setError(apiErrorText(err, t('yaml.applyError'))),
  })

  const handleCopy = () => copy(text)

  function reset() {
    setText(query.data ?? '')
    setPreview(null)
    setError(null)
    setEditing(false)
  }

  const dirty = isNew || text !== (query.data ?? '')
  const previewValid = preview !== null && previewFor === text

  const footer =
    canWrite && editing ? (
      <>
        {(error || (previewValid && preview) || applied) && (
          <div className="w-full space-y-2">
            {error && <Alert tone="danger">{error}</Alert>}
            {previewValid && preview && (
              <div className="rounded-md bg-neutral-soft px-3 py-2 text-xs">
                <p className="font-medium">{t('yaml.dryRunOk')}</p>
                <ul className="mt-1 space-y-0.5">
                  {preview.applied.map((a) => (
                    <li key={`${a.kind}/${a.namespace}/${a.name}`} className="flex items-center gap-2 font-mono">
                      <Badge tone={a.operation === 'created' ? 'success' : 'warning'}>{t(`yaml.op.${a.operation}`)}</Badge>
                      {a.kind} {a.namespace ? `${a.namespace}/` : ''}
                      {a.name}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {applied && <Alert tone="success">{t('yaml.applied', { count: applied.applied.length })}</Alert>}
          </div>
        )}
        {!isNew && (
          <Button variant="ghost" size="sm" onClick={reset} disabled={!dirty && !editing}>
            <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
            {t('yaml.discard')}
          </Button>
        )}
        <Button size="sm" onClick={() => dryRunMutation.mutate()} disabled={!dirty || dryRunMutation.isPending || !text.trim()}>
          {dryRunMutation.isPending ? t('yaml.validating') : t('yaml.dryRun')}
        </Button>
        <Button
          size="sm"
          variant="primary"
          onClick={() => applyMutation.mutate()}
          disabled={!previewValid || applyMutation.isPending}
          title={previewValid ? undefined : t('yaml.dryRunFirst')}
        >
          <Play className="h-3.5 w-3.5" aria-hidden="true" />
          {applyMutation.isPending ? t('yaml.applying') : t('yaml.apply')}
        </Button>
      </>
    ) : undefined

  const toolbar = (
      <div className="flex shrink-0 items-center justify-end gap-1 border-b border-border px-4 py-2">
        {!inline && (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setFullscreen((f) => !f)}
          title={fullscreen ? t('terminal.exitFullscreen') : t('terminal.fullscreen')}
          aria-label={fullscreen ? t('terminal.exitFullscreen') : t('terminal.fullscreen')}
          aria-pressed={fullscreen}
        >
          {fullscreen ? <Minimize2 aria-hidden="true" /> : <Maximize2 aria-hidden="true" />}
        </Button>
        )}
        <Button variant="ghost" size="sm" onClick={() => void handleCopy()} disabled={!text} title={t('common.copy')} aria-label={t('common.copy')}>
          {copied ? <Check className="text-success" aria-hidden="true" /> : <Copy aria-hidden="true" />}
        </Button>
        {canWrite && !editing && !isNew && (
          <Button size="sm" onClick={() => setEditing(true)}>
            <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
            {t('yaml.edit')}
          </Button>
        )}
      </div>
  )

  const body = (
    <>

      {!isNew && query.isLoading && <p className="p-4 text-sm text-fg-muted">{t('common.loading')}</p>}
      {!isNew && query.isError && (
        <Alert tone="danger" className="m-4">
          {t('yaml.error')}
        </Alert>
      )}
      {(isNew || query.data !== undefined) &&
        (editing ? (
          <CodeEditor
            value={text}
            onChange={(v) => {
              setText(v)
              setApplied(null)
            }}
          />
        ) : (
          <CodeEditor value={text} onChange={() => {}} readOnly />
        ))}
    </>
  )

  if (inline) {
    return (
      <div className="flex min-h-96 min-w-0 flex-1 flex-col overflow-hidden rounded-md border border-border bg-surface">
        {toolbar}
        {body}
        {footer && <div className="flex shrink-0 flex-wrap justify-end gap-2 border-t border-border px-4 py-3">{footer}</div>}
      </div>
    )
  }

  return (
    <Modal
      onClose={onClose}
      title={isNew ? t('yaml.newResource') : <span className="font-mono">{name}</span>}
      description={`${namespace ? `${namespace} · ` : ''}${editing ? t('yaml.editing') : t('yaml.title')}`}
      // The editor takes all the available height: no body padding or scroll,
      // CodeMirror itself handles scrolling.

      bodyClassName="flex flex-col overflow-hidden p-0"
      className={fullscreen ? 'max-w-none' : undefined}
      footer={footer}
    >
      {toolbar}
      {body}
    </Modal>
  )
}
