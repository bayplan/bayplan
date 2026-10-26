import type { TopologyEdge, TopologyNode } from '../api/topology'

// Deterministic layered layout, computed here instead of with a generic graph
// engine (elkjs, dagre): the graph always has the same shape (Ingress →
// Service → workload → pods → volumes), so fixed columns read better than any
// heuristic, cost nothing to bundle, and — above all — a node does not jump
// around when the 5-second refresh brings one more pod. Pure, so the order
// only depends on names.

export const NODE_WIDTH = 216
export const NODE_HEIGHT = 56
const COLUMN_GAP = 72
const ROW_HEIGHT = NODE_HEIGHT + 12
const GROUP_PAD_X = 16
const GROUP_PAD_TOP = 40
const GROUP_PAD_BOTTOM = 16
const GROUP_GAP = 24

const WORKLOAD_KINDS = ['Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet']

function column(kind: string): number {
  if (kind === 'Ingress') return 0
  if (kind === 'Service') return 1
  if (WORKLOAD_KINDS.includes(kind)) return 2
  if (kind === 'Pod') return 3
  return 4
}

export interface LaidOutNode {
  node: TopologyNode
  x: number
  y: number
  /** Pods folded into a workload when pods are hidden. */
  podCount?: number
}

export interface LaidOutGroup {
  namespace: string
  x: number
  y: number
  width: number
  height: number
  nodes: LaidOutNode[]
}

export interface TopologyLayout {
  groups: LaidOutGroup[]
  edges: TopologyEdge[]
}

/**
 * Hides pods that belong to a workload and reroutes their edges to it (a PVC
 * mounted by a StatefulSet's pod hangs from the StatefulSet). Bare pods stay:
 * folding them would make them vanish.
 */
export function foldPods(nodes: TopologyNode[], edges: TopologyEdge[]): { nodes: TopologyNode[]; edges: TopologyEdge[]; podCounts: Map<string, number> } {
  const ownerOf = new Map<string, string>()
  const podCounts = new Map<string, number>()
  for (const n of nodes) {
    if (n.kind === 'Pod' && n.owner) {
      ownerOf.set(n.id, n.owner)
      podCounts.set(n.owner, (podCounts.get(n.owner) ?? 0) + 1)
    }
  }
  const seen = new Set<string>()
  const folded: TopologyEdge[] = []
  for (const e of edges) {
    const source = ownerOf.get(e.source) ?? e.source
    const target = ownerOf.get(e.target) ?? e.target
    const key = `${source}>${target}`
    if (source === target || seen.has(key)) continue
    seen.add(key)
    folded.push({ ...e, source, target })
  }
  return { nodes: nodes.filter((n) => !ownerOf.has(n.id)), edges: folded, podCounts }
}

/** Places each value at its wanted row or right below the previous one, never overlapping. */
function settle(items: { id: string; want: number }[], start: number): Map<string, number> {
  const out = new Map<string, number>()
  let next = start
  const sorted = [...items].sort((a, b) => a.want - b.want || a.id.localeCompare(b.id))
  for (const it of sorted) {
    const row = Number.isFinite(it.want) ? Math.max(it.want, next) : next
    out.set(it.id, row)
    next = row + 1
  }
  return out
}

function mean(values: number[]): number {
  return values.length === 0 ? Infinity : values.reduce((a, b) => a + b, 0) / values.length
}

export function layoutTopology(nodes: TopologyNode[], edges: TopologyEdge[], showPods: boolean): TopologyLayout {
  let podCounts = new Map<string, number>()
  if (!showPods) {
    const folded = foldPods(nodes, edges)
    nodes = folded.nodes
    edges = folded.edges
    podCounts = folded.podCounts
  }

  // Columns no node uses (no Ingress, no PVC) are dropped so the graph does
  // not open with an empty band on the left.
  const used = [...new Set(nodes.map((n) => column(n.kind)))].sort((a, b) => a - b)
  const xOf = (kind: string) => GROUP_PAD_X + used.indexOf(column(kind)) * (NODE_WIDTH + COLUMN_GAP)
  const groupWidth = GROUP_PAD_X * 2 + Math.max(1, used.length) * NODE_WIDTH + Math.max(0, used.length - 1) * COLUMN_GAP

  const outgoing = new Map<string, string[]>()
  const incoming = new Map<string, string[]>()
  for (const e of edges) {
    outgoing.set(e.source, [...(outgoing.get(e.source) ?? []), e.target])
    incoming.set(e.target, [...(incoming.get(e.target) ?? []), e.source])
  }

  const byNamespace = new Map<string, TopologyNode[]>()
  for (const n of nodes) byNamespace.set(n.namespace, [...(byNamespace.get(n.namespace) ?? []), n])

  const groups: LaidOutGroup[] = []
  let groupY = 0
  for (const namespace of [...byNamespace.keys()].sort()) {
    const members = byNamespace.get(namespace)!
    const byName = (a: TopologyNode, b: TopologyNode) => a.name.localeCompare(b.name)
    const row = new Map<string, number>()

    // Workloads and their pods first: they decide the rows everything else
    // aligns with.
    let cursor = 0
    const workloads = members
      .filter((n) => WORKLOAD_KINDS.includes(n.kind))
      .sort((a, b) => WORKLOAD_KINDS.indexOf(a.kind) - WORKLOAD_KINDS.indexOf(b.kind) || byName(a, b))
    const pods = members.filter((n) => n.kind === 'Pod')
    for (const w of workloads) {
      const own = pods.filter((p) => p.owner === w.id).sort(byName)
      own.forEach((p, i) => row.set(p.id, cursor + i))
      const block = Math.max(1, own.length)
      row.set(w.id, cursor + (block - 1) / 2)
      cursor += block
    }
    for (const p of pods.filter((p) => !row.has(p.id)).sort(byName)) row.set(p.id, cursor++)

    const place = (kind: (n: TopologyNode) => boolean, anchors: (n: TopologyNode) => string[]) => {
      const items = members.filter(kind).map((n) => ({ id: n.id, want: mean(anchors(n).filter((a) => row.has(a)).map((a) => row.get(a)!)) }))
      for (const [id, r] of settle(items, 0)) row.set(id, r)
    }
    place((n) => n.kind === 'Service', (n) => outgoing.get(n.id) ?? [])
    place((n) => n.kind === 'Ingress', (n) => outgoing.get(n.id) ?? [])
    place((n) => column(n.kind) === 4, (n) => incoming.get(n.id) ?? [])

    const rows = Math.max(1, ...[...row.values()].map((r) => r + 1))
    groups.push({
      namespace,
      x: 0,
      y: groupY,
      width: groupWidth,
      height: GROUP_PAD_TOP + rows * ROW_HEIGHT - (ROW_HEIGHT - NODE_HEIGHT) + GROUP_PAD_BOTTOM,
      nodes: members.map((n) => ({ node: n, x: xOf(n.kind), y: GROUP_PAD_TOP + (row.get(n.id) ?? 0) * ROW_HEIGHT, podCount: podCounts.get(n.id) })),
    })
    groupY += groups[groups.length - 1].height + GROUP_GAP
  }
  return { groups, edges }
}

/** Everything reachable downstream and upstream of `ids`: what they depend on and what depends on them. */
export function relatedNodes(ids: string[], edges: TopologyEdge[]): Set<string> {
  const out = new Set(ids)
  const walk = (from: 'source' | 'target', to: 'source' | 'target') => {
    const seen = new Set(ids)
    const queue = [...ids]
    while (queue.length > 0) {
      const current = queue.pop()!
      for (const e of edges) {
        if (e[from] === current && !seen.has(e[to])) {
          seen.add(e[to])
          out.add(e[to])
          queue.push(e[to])
        }
      }
    }
  }
  walk('source', 'target')
  walk('target', 'source')
  return out
}
