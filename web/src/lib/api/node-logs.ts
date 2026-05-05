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

export function createNodeLogsApi() {
  return {
    async queryNodeLogs(
      token: string,
      q: NodeLogQuery,
      options?: { signal?: AbortSignal },
    ): Promise<NodeLogQueryResult> {
      return request<NodeLogQueryResult>(`/node-logs${buildQuery(q)}`, {
        token,
        signal: options?.signal,
      })
    },

    async getAlertLogs(
      token: string,
      alertId: number,
      options?: { signal?: AbortSignal },
    ): Promise<AlertLogsResult> {
      return request<AlertLogsResult>(`/alerts/${alertId}/logs`, {
        token,
        signal: options?.signal,
      })
    },

    async getNodeLogConfig(
      token: string,
      nodeId: number,
      options?: { signal?: AbortSignal },
    ): Promise<NodeLogConfig> {
      return request<NodeLogConfig>(`/nodes/${nodeId}/log-config`, {
        token,
        signal: options?.signal,
      })
    },

    async updateNodeLogConfig(
      token: string,
      nodeId: number,
      config: NodeLogConfig,
    ): Promise<NodeLogConfig> {
      return request<NodeLogConfig>(`/nodes/${nodeId}/log-config`, {
        token,
        method: "PATCH",
        body: config,
      })
    },

    async getLogsSettings(
      token: string,
      options?: { signal?: AbortSignal },
    ): Promise<NodeLogsSettings> {
      return request<NodeLogsSettings>("/settings/logs", {
        token,
        signal: options?.signal,
      })
    },

    async updateLogsSettings(
      token: string,
      s: NodeLogsSettings,
    ): Promise<NodeLogsSettings> {
      return request<NodeLogsSettings>("/settings/logs", {
        token,
        method: "PATCH",
        body: s,
      })
    },
  }
}
