import { request } from "./core"
import type {
  AlertLogsResult,
  NodeLogConfig,
  NodeLogEntry,
  NodeLogPriority,
  NodeLogQueryResult,
  NodeLogSource,
  NodeLogsSettings,
} from "@/types/domain"

export type NodeLogQuery = {
  nodeIds?: number[]
  source?: NodeLogSource[]
  path?: string
  priority?: string[]
  start?: string
  end?: string
  q?: string
  page?: number
  pageSize?: number
}

type RawNodeLogEntry = {
  id: number
  node_id?: number
  source?: string
  path?: string
  timestamp?: string
  priority?: string
  message?: string
  created_at?: string
}

type RawNodeLogQueryResult = {
  data?: RawNodeLogEntry[]
  total?: number
  has_more?: boolean
}

type RawNodeLogConfig = {
  log_paths?: string[]
  log_journalctl_enabled?: boolean
  log_retention_days?: number
}

type RawAlertLogsResult = {
  data?: RawNodeLogEntry[]
  node_id?: number
  window_start?: string
  window_end?: string
  hint?: string
}

type RawNodeLogsSettings = {
  default_retention_days?: number
}

function mapSource(raw?: string): NodeLogSource {
  return raw === "file" ? "file" : "journalctl"
}

function mapPriority(raw?: string): NodeLogPriority {
  switch (raw) {
    case "emerg":
    case "alert":
    case "crit":
    case "err":
    case "warning":
    case "notice":
    case "info":
    case "debug":
    case "":
      return raw
    default:
      return ""
  }
}

function mapLogEntry(row: RawNodeLogEntry): NodeLogEntry {
  return {
    id: row.id,
    nodeId: Number(row.node_id) || 0,
    source: mapSource(row.source),
    path: String(row.path ?? ""),
    timestamp: String(row.timestamp ?? ""),
    priority: mapPriority(row.priority),
    message: String(row.message ?? ""),
    createdAt: String(row.created_at ?? ""),
  }
}

function mapQueryResult(row: RawNodeLogQueryResult): NodeLogQueryResult {
  return {
    data: Array.isArray(row.data) ? row.data.map(mapLogEntry) : [],
    total: Number(row.total) || 0,
    hasMore: Boolean(row.has_more),
  }
}

function mapConfig(row: RawNodeLogConfig): NodeLogConfig {
  return {
    logPaths: Array.isArray(row.log_paths) ? row.log_paths : [],
    logJournalctlEnabled: Boolean(row.log_journalctl_enabled),
    logRetentionDays: Number(row.log_retention_days) || 0,
  }
}

function mapAlertLogs(row: RawAlertLogsResult): AlertLogsResult {
  return {
    data: Array.isArray(row.data) ? row.data.map(mapLogEntry) : [],
    nodeId: Number(row.node_id) || 0,
    windowStart: String(row.window_start ?? ""),
    windowEnd: String(row.window_end ?? ""),
    hint: row.hint,
  }
}

function mapSettings(row: RawNodeLogsSettings): NodeLogsSettings {
  return {
    defaultRetentionDays: Number(row.default_retention_days) || 0,
  }
}

function buildQuery(q: NodeLogQuery): string {
  const params = new URLSearchParams()
  if (q.nodeIds?.length) params.set("node_ids", q.nodeIds.join(","))
  if (q.source?.length) params.set("source", q.source.join(","))
  if (q.path) params.set("path", q.path)
  if (q.priority?.length) params.set("priority", q.priority.join(","))
  if (q.start) params.set("start", q.start)
  if (q.end) params.set("end", q.end)
  if (q.q) params.set("q", q.q)
  if (q.page) params.set("page", String(q.page))
  if (q.pageSize) params.set("page_size", String(q.pageSize))
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
      const row = await request<RawNodeLogQueryResult>(`/node-logs${buildQuery(q)}`, {
        token,
        signal: options?.signal,
      })
      return mapQueryResult(row)
    },

    async getAlertLogs(
      token: string,
      alertId: number,
      options?: { signal?: AbortSignal },
    ): Promise<AlertLogsResult> {
      const row = await request<RawAlertLogsResult>(`/alerts/${alertId}/logs`, {
        token,
        signal: options?.signal,
      })
      return mapAlertLogs(row)
    },

    async getNodeLogConfig(
      token: string,
      nodeId: number,
      options?: { signal?: AbortSignal },
    ): Promise<NodeLogConfig> {
      const row = await request<RawNodeLogConfig>(`/nodes/${nodeId}/log-config`, {
        token,
        signal: options?.signal,
      })
      return mapConfig(row)
    },

    async updateNodeLogConfig(
      token: string,
      nodeId: number,
      config: NodeLogConfig,
    ): Promise<NodeLogConfig> {
      const row = await request<RawNodeLogConfig>(`/nodes/${nodeId}/log-config`, {
        token,
        method: "PATCH",
        body: {
          log_paths: config.logPaths,
          log_journalctl_enabled: config.logJournalctlEnabled,
          log_retention_days: config.logRetentionDays,
        },
      })
      return mapConfig(row)
    },

    async getLogsSettings(
      token: string,
      options?: { signal?: AbortSignal },
    ): Promise<NodeLogsSettings> {
      const row = await request<RawNodeLogsSettings>("/settings/logs", {
        token,
        signal: options?.signal,
      })
      return mapSettings(row)
    },

    async updateLogsSettings(
      token: string,
      s: NodeLogsSettings,
    ): Promise<NodeLogsSettings> {
      const row = await request<RawNodeLogsSettings>("/settings/logs", {
        token,
        method: "PATCH",
        body: { default_retention_days: s.defaultRetentionDays },
      })
      return mapSettings(row)
    },
  }
}
