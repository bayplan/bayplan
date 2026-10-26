import { api } from '../lib/api'

export interface DockerInfo {
  id: string
  name: string
  serverVersion: string
  operatingSystem: string
  osType: string
  architecture: string
  kernelVersion: string
  cpus: number
  memTotalBytes: number
  containers: number
  containersRunning: number
  containersPaused: number
  containersStopped: number
  images: number
  storageDriver: string
  swarm: boolean
  podman: boolean
}

export interface DockerPort {
  ip?: string
  privatePort: number
  publicPort?: number
  type: string
}

export interface DockerContainer {
  id: string
  name: string
  image: string
  command: string
  created: string
  state: 'running' | 'exited' | 'paused' | 'created' | 'restarting' | 'dead' | string
  status: string
  ports: DockerPort[]
  labels: Record<string, string>
  stack?: string
}

export interface DockerImage {
  id: string
  tags: string[]
  sizeBytes: number
  created: string
  inUse: boolean
  dangling: boolean
}

export interface DockerVolume {
  name: string
  driver: string
  mountpoint: string
  created?: string
  labels: Record<string, string>
  stack?: string
}

export interface DockerNetwork {
  id: string
  name: string
  driver: string
  scope: string
  internal: boolean
  subnets: string[]
  labels: Record<string, string>
  stack?: string
}

export type ContainerAction = 'start' | 'stop' | 'restart' | 'kill' | 'pause' | 'unpause'

const base = (clusterId: number) => `/clusters/${clusterId}/docker`

export function getDockerInfo(clusterId: number) {
  return api.get<DockerInfo>(`${base(clusterId)}/info`).then((r) => r.data)
}
export function listContainers(clusterId: number) {
  return api.get<DockerContainer[]>(`${base(clusterId)}/containers`).then((r) => r.data)
}
export function inspectContainer(clusterId: number, id: string) {
  return api.get<unknown>(`${base(clusterId)}/containers/${id}/inspect`).then((r) => JSON.stringify(r.data, null, 2))
}
export function containerAction(clusterId: number, id: string, action: ContainerAction) {
  return api.post(`${base(clusterId)}/containers/${id}/action/${action}`)
}
export function removeContainer(clusterId: number, id: string, opts: { force?: boolean; volumes?: boolean } = {}) {
  return api.delete(`${base(clusterId)}/containers/${id}`, { params: { force: opts.force ? '1' : undefined, volumes: opts.volumes ? '1' : undefined } })
}
export function listImages(clusterId: number) {
  return api.get<DockerImage[]>(`${base(clusterId)}/images`).then((r) => r.data)
}
export function pullImage(clusterId: number, ref: string) {
  return api.post(`${base(clusterId)}/images/pull`, { ref }, { timeout: 10 * 60 * 1000 })
}
export function removeImage(clusterId: number, id: string, force = false) {
  return api.delete(`${base(clusterId)}/images/${encodeURIComponent(id)}`, { params: { force: force ? '1' : undefined } })
}
export function listVolumes(clusterId: number) {
  return api.get<DockerVolume[]>(`${base(clusterId)}/volumes`).then((r) => r.data)
}
export function removeVolume(clusterId: number, name: string) {
  return api.delete(`${base(clusterId)}/volumes/${encodeURIComponent(name)}`)
}
export function listNetworks(clusterId: number) {
  return api.get<DockerNetwork[]>(`${base(clusterId)}/networks`).then((r) => r.data)
}
export function removeNetwork(clusterId: number, id: string) {
  return api.delete(`${base(clusterId)}/networks/${id}`)
}

function wsBase(clusterId: number) {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws/clusters/${clusterId}/docker/containers`
}

export function openContainerLogsSocket(clusterId: number, id: string): WebSocket {
  return new WebSocket(`${wsBase(clusterId)}/${id}/logs`)
}

export function openContainerExecSocket(clusterId: number, id: string, command: string): WebSocket {
  const url = new URL(`${wsBase(clusterId)}/${id}/exec`)
  if (command) url.searchParams.set('command', command)
  const ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'
  return ws
}
