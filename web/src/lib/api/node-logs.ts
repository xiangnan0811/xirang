import { request } from "./core"
import type {
  AlertLogsResult,
  NodeLogConfig,
  NodeLogQueryResult,
  NodeLogsSettings,
} from "@/types/domain"

export type NodeLogQuery = {
  node_ids?: number[]
  source?: ("journalctl" | "file")[]
  path?: string
  priority?: string[]
  start?: string
  end?: string
  q?: string
  page?: number
  page_size?: number
}

function buildQuery(q: NodeLogQuery): string {
  const params = new URLSearchParams()
  if (q.node_ids?.length) params.set("node_ids", q.node_ids.join(","))
  if (q.source?.length) params.set("source", q.source.join(","))
  if (q.path) params.set("path", q.path)
  if (q.priority?.length) params.set("priority", q.priority.join(","))
  if (q.start) params.set("start", q.start)
  if (q.end) params.set("end", q.end)
  if (q.q) params.set("q", q.q)
  if (q.page) params.set("page", String(q.page))
  if (q.page_size) params.set("page_size", String(q.page_size))
  const s = params.toString()
  return s ? `?${s}` : ""
}

export const queryNodeLogs = (token: string, q: NodeLogQuery) =>
  request<NodeLogQueryResult>(`/node-logs${buildQuery(q)}`, { token })

export const getAlertLogs = (token: string, alertId: number) =>
  request<AlertLogsResult>(`/alerts/${alertId}/logs`, { token })

export const getNodeLogConfig = (token: string, nodeId: number) =>
  request<NodeLogConfig>(`/nodes/${nodeId}/log-config`, { token })

export const updateNodeLogConfig = (
  token: string,
  nodeId: number,
  config: NodeLogConfig,
) =>
  request<NodeLogConfig>(`/nodes/${nodeId}/log-config`, {
    token,
    method: "PATCH",
    body: config,
  })

export const getLogsSettings = (token: string) =>
  request<NodeLogsSettings>("/settings/logs", { token })

export const updateLogsSettings = (token: string, s: NodeLogsSettings) =>
  request<NodeLogsSettings>("/settings/logs", {
    token,
    method: "PATCH",
    body: s,
  })
