import type { BadgeTone } from '../components/ui'
import type { ClusterStatus } from '../api/clusters'

// Pod statuses that are a real failure and not a transition. The backend
// already derives the concrete reason (the waiting.reason of the first
// container that fails to start, see k8s/read.go) and until now the UI threw
// it away: a CrashLoopBackOff was painted the same amber as a Pending, i.e. a
// broken pod and a starting pod looked the same.
const POD_ERROR_STATUSES = new Set([
  'CrashLoopBackOff',
  'ImagePullBackOff',
  'ErrImagePull',
  'InvalidImageName',
  'CreateContainerConfigError',
  'CreateContainerError',
  'RunContainerError',
  'OOMKilled',
  'DeadlineExceeded',
  'Evicted',
  'Error',
  'Failed',
])

const POD_OK_STATUSES = new Set(['Running', 'Succeeded', 'Completed'])

/**
 * Colour of a pod's status, with the precedence
 * `error > transition > state`: what is broken always wins.
 *
 * An unknown status falls into `warning`, never `success`: the list of
 * Kubernetes waiting reasons grows with every version and painting green
 * something we do not know how to interpret is the one failure that cannot be
 * allowed here.
 */
export function podTone(status: string): BadgeTone {
  if (POD_ERROR_STATUSES.has(status)) return 'danger'
  if (POD_OK_STATUSES.has(status)) return 'success'
  return 'warning'
}

/**
 * Colour of a deployment's rollout. This column used to have no colour at
 * all: a "0/3" read the same as a "3/3".
 */
export function deploymentTone(d: { replicas: number; readyReplicas: number }): BadgeTone {
  // Scaled to 0 by hand is not a failure, it is a decision: neutral, not red.
  if (d.replicas === 0) return 'neutral'
  if (d.readyReplicas === 0) return 'danger'
  if (d.readyReplicas < d.replicas) return 'warning'
  return 'success'
}

/**
 * Colour of a node's status. `Unknown` goes amber and not red: it is what a
 * node that has stopped reporting shows, and in practice it is usually a spot
 * interruption or a scale-down in progress rather than a broken node.
 * Red is reserved for NotReady, which is a node that does respond and says it
 * is not fine.
 */
export function nodeTone(status: string): BadgeTone {
  if (status === 'Ready') return 'success'
  if (status === 'Unknown') return 'warning'
  return 'danger'
}

/** Tone of a role: admin stands out, editor warns that it writes, readonly neutral. */
export function roleTone(role: string | undefined): BadgeTone {
  if (role === 'admin') return 'success'
  if (role === 'editor') return 'warning'
  return 'neutral'
}

export type ClusterConnection = 'ok' | 'connecting' | 'error' | 'disabled' | 'unknown'

const CONNECTION_TONES: Record<ClusterConnection, BadgeTone> = {
  ok: 'success',
  connecting: 'warning',
  error: 'danger',
  disabled: 'neutral',
  unknown: 'neutral',
}

export function connectionTone(connection: ClusterConnection): BadgeTone {
  return CONNECTION_TONES[connection]
}

/**
 * i18n key of a connection state. `unknown` (the /readyz query still in
 * flight) is presented as "connecting": it is what is actually happening, and
 * it avoids an "unknown" flashing on every page load.
 */
export function connectionLabelKey(connection: ClusterConnection): string {
  return `common.connection.${connection === 'unknown' ? 'connecting' : connection}`
}

/**
 * Connection state of a cluster from the status the backend keeps in memory
 * and returns in /api/clusters (live check every 30 s plus the informer cache
 * state).
 *
 * This exists because a cluster that was down looked **exactly like an empty
 * cluster**: the tables showed 0 rows and nobody said why. It used to be
 * cross-referenced by name with /readyz; now the data travels with the cluster
 * itself.
 *
 * Precedence: what is broken wins. A recent error is shown even if the cache is
 * still synced (expired token with stale data).
 */
export function clusterConnection(cluster: { enabled: boolean; status?: ClusterStatus }): ClusterConnection {
  if (!cluster.enabled) return 'disabled'
  const st = cluster.status
  if (!st) return 'unknown'
  // Enabled but the backend has not registered it yet: happens in the window
  // between enabling it and Connect creating the connection.
  if (!st.registered) return 'connecting'
  if (st.lastError) return 'error'
  if (st.synced && st.connected) return 'ok'
  // Synced but the first health check has not come back yet: not an error.
  if (st.synced && !st.lastCheckedAt) return 'ok'
  return 'connecting'
}

export type AgeUnit = 'seconds' | 'minutes' | 'hours' | 'days'

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * Age of a resource, in the largest unit that yields a number >= 1 (kubectl
 * style: `5d`, `3h`, `45m`). Returns the number and the unit separately so the
 * translation is done by whoever renders, not by this function.
 *
 * `createdAt` already travelled in the backend's four DTOs and was not shown in
 * any table. Returns null if the date is not parseable, so the cell stays empty
 * instead of painting "NaN".
 */
export function ageParts(iso: string, now: number = Date.now()): { value: number; unit: AgeUnit } | null {
  const created = new Date(iso).getTime()
  if (Number.isNaN(created)) return null
  // A client clock running ahead would give a negative age: clamped to 0.
  const ms = Math.max(0, now - created)
  if (ms >= DAY) return { value: Math.floor(ms / DAY), unit: 'days' }
  if (ms >= HOUR) return { value: Math.floor(ms / HOUR), unit: 'hours' }
  if (ms >= MINUTE) return { value: Math.floor(ms / MINUTE), unit: 'minutes' }
  return { value: Math.floor(ms / SECOND), unit: 'seconds' }
}

// Thresholds of RESERVED capacity (requests), not consumption: reserving a lot
// is not a failure, so amber and red are kept for when there is no room left to
// schedule new pods (a pod that does not fit stays Pending).
export function capacityTone(pct: number): BadgeTone {
  if (pct >= 90) return 'danger'
  if (pct >= 75) return 'warning'
  return 'success'
}

/**
 * Tone of a Helm release status. `deployed` is the only green; `failed` red;
 * everything else (pending-*, superseded, uninstalling, unknown) amber: as
 * everywhere else, what we do not know how to interpret is never green.
 */
export function helmReleaseTone(status: string): BadgeTone {
  if (status === 'deployed') return 'success'
  if (status === 'failed') return 'danger'
  return 'warning'
}

/** Tone of a Docker container state. Only `running` is green; stopped/exited neutral; the rest amber/red. */
export function containerTone(state: string): BadgeTone {
  switch (state) {
    case 'running':
      return 'success'
    case 'exited':
    case 'created':
      return 'neutral'
    case 'dead':
      return 'danger'
    default:
      return 'warning'
  }
}
