import { api } from '../lib/api'

export interface AIStatus {
  /** Enabled with a provider, a model and (where needed) a key. */
  ready: boolean
  provider: string
  model: string
  minRole: 'readonly' | 'editor' | 'admin'
  dailyLimit: number
}

export function getAIStatus() {
  return api.get<AIStatus>('/ai/status').then((r) => r.data)
}

export interface ExplainSection {
  title: string
  /** What was sent, in a few words (the content itself never comes back). */
  summary: string
}

export interface Explanation {
  text: string
  provider: string
  model: string
  sent: ExplainSection[]
  inputTokens: number
  outputTokens: number
  durationMs: number
  remainingToday: number
}

export interface ExplainTarget {
  /** Route kind, plural (pods, deployments…). */
  kind: string
  namespace?: string
  name: string
}

export function explain(clusterId: number, target: ExplainTarget, lang: string) {
  return api.post<Explanation>(`/clusters/${clusterId}/explain`, { ...target, lang }).then((r) => r.data)
}

/** Kubernetes kind of an attention issue -> the route kind Explain takes; undefined = not explainable. */
export function explainKindFor(kind: string): string | undefined {
  return (
    {
      Pod: 'pods',
      Deployment: 'deployments',
      StatefulSet: 'statefulsets',
      DaemonSet: 'daemonsets',
      Service: 'services',
      Ingress: 'ingresses',
      PersistentVolumeClaim: 'persistentvolumeclaims',
      Node: 'nodes',
      Job: 'jobs',
      CronJob: 'cronjobs',
    } as Record<string, string>
  )[kind]
}

/** Route kinds (plural) Explain accepts, the same list as the server's. */
export function isExplainableKind(kind: string): boolean {
  return ['pods', 'deployments', 'statefulsets', 'daemonsets', 'services', 'ingresses', 'persistentvolumeclaims', 'nodes', 'jobs', 'cronjobs'].includes(kind)
}
