import type {
  AlertDeliveryRecord,
  AlertBulkRetryResult,
  AlertDeliveryRetryResult,
  AlertDeliveryStats,
  AuditLogRecord,
  AlertRecord,
  IntegrationChannel,
  IntegrationProbeResult,
  LogEvent,
  LoginResponse,
  NewIntegrationInput,
  NewNodeInput,
  NewPolicyInput,
  NewSSHKeyInput,
  NewTaskInput,
  NodeRecord,
  NodeStatus,
  PolicyRecord,
  SSHKeyRecord,
  TaskRecord,
  TaskStatus
} from "@/types/domain";

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "/api/v1";
const DEV_DIRECT_API_BASE_URL = import.meta.env.VITE_DEV_API_DIRECT_URL ?? "http://127.0.0.1:8080/api/v1";

export class ApiError extends Error {
  status: number;
  detail?: unknown;

  constructor(status: number, message: string, detail?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.detail = detail;
  }
}

type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  token?: string;
  signal?: AbortSignal;
};

type Envelope<T> = {
  data?: T;
  message?: string;
  error?: string;
};

type NodeResponse = {
  id: number;
  name: string;
  host: string;
  port?: number;
  username?: string;
  auth_type?: "key" | "password";
  ssh_key_id?: number | null;
  tags?: string;
  status?: string;
  base_path?: string;
  last_seen_at?: string | null;
  last_backup_at?: string | null;
  connection_latency_ms?: number;
  disk_used_gb?: number;
  disk_total_gb?: number;
};

type PolicyResponse = {
  id: number;
  name: string;
  source_path: string;
  target_path: string;
  cron_spec: string;
  enabled: boolean;
};

type TaskResponse = {
  id: number;
  name: string;
  status: string;
  command?: string;
  rsync_source?: string;
  rsync_target?: string;
  executor_type?: string;
  cron_spec?: string;
  policy_id?: number | null;
  retry_count?: number;
  last_error?: string;
  node_id?: number;
  node?: {
    id?: number;
    name?: string;
  };
  policy?: {
    id?: number;
    name?: string;
  };
  last_run_at?: string | null;
  next_run_at?: string | null;
  created_at?: string;
  updated_at?: string;
};

type TaskLogResponse = {
  id: number;
  task_id: number;
  level: string;
  message: string;
  created_at: string;
};

type SSHKeyResponse = {
  id: number;
  name: string;
  username: string;
  key_type?: "auto" | "rsa" | "ed25519" | "ecdsa";
  private_key?: string;
  fingerprint: string;
  created_at: string;
  last_used_at?: string | null;
};

type IntegrationResponse = {
  id: number;
  type: IntegrationChannel["type"];
  name: string;
  endpoint: string;
  enabled: boolean;
  fail_threshold: number;
  cooldown_minutes: number;
};

type AlertResponse = {
  id: number;
  node_id: number;
  node_name: string;
  task_id?: number | null;
  policy_name?: string;
  severity: AlertRecord["severity"];
  status: AlertRecord["status"];
  error_code: string;
  message: string;
  retryable: boolean;
  triggered_at: string;
};

type AlertDeliveryResponse = {
  id: number;
  alert_id: number;
  integration_id: number;
  status: "sent" | "failed";
  error?: string;
  created_at: string;
};

type IntegrationTestResponse = {
  ok: boolean;
  message: string;
  latency_ms?: number;
};

type RetryAlertDeliveryResponse = {
  ok: boolean;
  message: string;
  delivery: AlertDeliveryResponse;
};

type RetryFailedDeliveriesResponse = {
  ok: boolean;
  message: string;
  total_failed: number;
  success_count: number;
  failed_count: number;
  new_deliveries: AlertDeliveryResponse[];
};

type DeliveryStatsIntegrationResponse = {
  integration_id: number;
  name: string;
  type: string;
  sent: number;
  failed: number;
};

type DeliveryStatsResponse = {
  window_hours: number;
  total_sent: number;
  total_failed: number;
  success_rate: number;
  by_integration: DeliveryStatsIntegrationResponse[];
};

type AuditLogResponse = {
  id: number;
  user_id: number;
  username: string;
  role: string;
  method: string;
  path: string;
  status_code: number;
  client_ip: string;
  user_agent: string;
  created_at: string;
};

type TestNodeResponse = {
  ok: boolean;
  message: string;
  latency_ms?: number;
  disk_used_gb?: number;
  disk_total_gb?: number;
};

type NodeBatchDeleteResponse = {
  deleted?: number;
  not_found_ids?: number[];
  message?: string;
};

function shouldTryDirectFallback(baseUrl: string): boolean {
  if (typeof window === "undefined") {
    return false;
  }
  const isLocalhost = window.location.hostname === "localhost" || window.location.hostname === "127.0.0.1";
  return isLocalhost && baseUrl.startsWith("/");
}

async function doFetch(baseUrl: string, path: string, options: RequestOptions): Promise<Response> {
  return fetch(`${baseUrl}${path}`, {
    method: options.method ?? "GET",
    headers: {
      "Content-Type": "application/json",
      ...(options.token ? { Authorization: `Bearer ${options.token}` } : {})
    },
    body: options.body ? JSON.stringify(options.body) : undefined,
    signal: options.signal
  });
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? "GET";
  const isWriteOperation = method !== "GET";
  let response: Response;

  try {
    response = await doFetch(API_BASE_URL, path, options);
  } catch (error) {
    if (isWriteOperation || !shouldTryDirectFallback(API_BASE_URL)) {
      throw error;
    }
    response = await doFetch(DEV_DIRECT_API_BASE_URL, path, options);
  }

  if (response.status === 404 && !isWriteOperation && shouldTryDirectFallback(API_BASE_URL)) {
    try {
      response = await doFetch(DEV_DIRECT_API_BASE_URL, path, options);
    } catch {
      // 保留原始 404 响应，避免吞掉错误上下文
    }
  }

  const text = await response.text();
  let payload: unknown = null;

  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = text;
    }
  }

  if (!response.ok) {
    throw new ApiError(response.status, `请求失败：${response.status}`, payload);
  }

  if (payload && typeof payload === "object") {
    return payload as T;
  }

  return payload as T;
}

async function fetchWithFallback(url: string, options: RequestInit): Promise<Response> {
  let response: Response;
  try {
    response = await fetch(`${API_BASE_URL}${url}`, options);
  } catch (error) {
    if (!shouldTryDirectFallback(API_BASE_URL)) {
      throw error;
    }
    response = await fetch(`${DEV_DIRECT_API_BASE_URL}${url}`, options);
  }

  if (response.status === 404 && shouldTryDirectFallback(API_BASE_URL)) {
    try {
      response = await fetch(`${DEV_DIRECT_API_BASE_URL}${url}`, options);
    } catch {
      // 保留原始 404 响应
    }
  }

  return response;
}

function unwrapData<T>(payload: Envelope<T> | T): T {
  if (payload && typeof payload === "object" && "data" in (payload as Record<string, unknown>)) {
    return ((payload as Envelope<T>).data ?? null) as T;
  }
  return payload as T;
}

function mapNodeStatus(raw?: string): NodeStatus {
  switch (raw) {
    case "online":
      return "online";
    case "warning":
      return "warning";
    default:
      return "offline";
  }
}

function mapTaskStatus(raw: string): TaskStatus {
  switch (raw) {
    case "running":
    case "pending":
    case "failed":
    case "success":
    case "retrying":
    case "canceled":
      return raw;
    default:
      return "pending";
  }
}

function mapTaskExecutor(raw?: string): TaskRecord["executorType"] {
  return raw === "rsync" ? raw : "rsync";
}

function mapLogLevel(raw?: string): LogEvent["level"] {
  if (raw === "error") {
    return "error";
  }
  if (raw === "warn" || raw === "warning") {
    return "warn";
  }
  return "info";
}

function deriveTaskProgress(status: TaskStatus, retryCount: number, index: number) {
  switch (status) {
    case "success":
      return 100;
    case "running":
      return 32 + (index * 11) % 52;
    case "retrying":
      return Math.min(95, 18 + retryCount * 15 + (index % 13));
    case "failed":
      return 68;
    case "canceled":
      return 0;
    default:
      return 0;
  }
}

function extractErrorCode(message?: string) {
  if (!message) {
    return undefined;
  }
  const matched = message.match(/XR-[A-Z]+-\d+/);
  return matched?.[0];
}

function formatTime(input?: string | null): string {
  if (!input) {
    return "-";
  }
  const date = new Date(input);
  if (Number.isNaN(date.getTime())) {
    return input;
  }
  return date.toLocaleString("zh-CN", { hour12: false });
}

function mapNode(row: NodeResponse): NodeRecord {
  const diskTotalGb = row.disk_total_gb && row.disk_total_gb > 0 ? row.disk_total_gb : 0;
  const diskUsedGb = row.disk_used_gb && row.disk_used_gb >= 0 ? row.disk_used_gb : 0;
  const freePercent = diskTotalGb > 0
    ? Math.max(0, Math.round(((diskTotalGb - diskUsedGb) / diskTotalGb) * 100))
    : 0;

  return {
    id: row.id,
    name: row.name,
    host: row.host,
    address: row.host,
    ip: row.host,
    port: row.port ?? 22,
    username: row.username ?? "root",
    authType: row.auth_type ?? "key",
    keyId: row.ssh_key_id ? `key-${row.ssh_key_id}` : null,
    basePath: row.base_path ?? "/",
    status: mapNodeStatus(row.status),
    tags: row.tags ? row.tags.split(",").map((one) => one.trim()).filter(Boolean) : [],
    lastSeenAt: formatTime(row.last_seen_at),
    lastBackupAt: formatTime(row.last_backup_at),
    diskFreePercent: freePercent,
    diskUsedGb,
    diskTotalGb,
    successRate: row.status === "online" ? 100 : row.status === "warning" ? 75 : 0,
    diskProbeAt: formatTime(row.last_seen_at),
    connectionLatencyMs: row.connection_latency_ms
  };
}

function mapPolicy(row: PolicyResponse): PolicyRecord {
  return {
    id: row.id,
    name: row.name,
    sourcePath: row.source_path,
    targetPath: row.target_path,
    cron: row.cron_spec,
    naturalLanguage: `按照 ${row.cron_spec} 调度`,
    enabled: row.enabled,
    criticalThreshold: 2
  };
}

function mapSSHKey(row: SSHKeyResponse): SSHKeyRecord {
  return {
    id: `key-${row.id}`,
    name: row.name,
    username: row.username,
    keyType: row.key_type ?? "auto",
    fingerprint: row.fingerprint,
    createdAt: formatTime(row.created_at),
    lastUsedAt: formatTime(row.last_used_at)
  };
}

function mapIntegration(row: IntegrationResponse): IntegrationChannel {
  return {
    id: `int-${row.id}`,
    type: row.type,
    name: row.name,
    endpoint: row.endpoint,
    enabled: row.enabled,
    failThreshold: row.fail_threshold,
    cooldownMinutes: row.cooldown_minutes
  };
}

function mapAlert(row: AlertResponse): AlertRecord {
  return {
    id: `alert-${row.id}`,
    nodeName: row.node_name,
    nodeId: row.node_id,
    taskId: row.task_id ?? null,
    policyName: row.policy_name ?? "节点探测",
    severity: row.severity,
    status: row.status,
    errorCode: row.error_code,
    message: row.message,
    triggeredAt: formatTime(row.triggered_at),
    retryable: row.retryable
  };
}

function mapAlertDelivery(row: AlertDeliveryResponse): AlertDeliveryRecord {
  return {
    id: `delivery-${row.id}`,
    alertId: `alert-${row.alert_id}`,
    integrationId: `int-${row.integration_id}`,
    status: row.status === "failed" ? "failed" : "sent",
    error: row.error || undefined,
    createdAt: formatTime(row.created_at)
  };
}

function mapDeliveryStats(payload?: DeliveryStatsResponse | null): AlertDeliveryStats {
  if (!payload) {
    return {
      windowHours: 24,
      totalSent: 0,
      totalFailed: 0,
      successRate: 0,
      byIntegration: []
    };
  }

  return {
    windowHours: Number(payload.window_hours || 24),
    totalSent: Number(payload.total_sent || 0),
    totalFailed: Number(payload.total_failed || 0),
    successRate: Number(payload.success_rate || 0),
    byIntegration: Array.isArray(payload.by_integration)
      ? payload.by_integration.map((item) => {
          const sent = Number(item.sent || 0);
          const failed = Number(item.failed || 0);
          const total = sent + failed;
          const successRate = total > 0 ? Number(((sent / total) * 100).toFixed(1)) : 0;
          return {
            integrationId: `int-${item.integration_id}`,
            name: item.name || `integration-${item.integration_id}`,
            type: item.type || "webhook",
            sent,
            failed,
            successRate
          };
        })
      : []
  };
}

function mapAuditLog(row: AuditLogResponse): AuditLogRecord {
  return {
    id: row.id,
    userId: row.user_id,
    username: row.username,
    role: row.role,
    method: row.method,
    path: row.path,
    statusCode: row.status_code,
    clientIP: row.client_ip,
    userAgent: row.user_agent,
    createdAt: formatTime(row.created_at)
  };
}

function mapTask(row: TaskResponse, index: number): TaskRecord {
  const status = mapTaskStatus(row.status);
  const retryCount = row.retry_count ?? 0;
  const errorCode = status === "failed" ? extractErrorCode(row.last_error) : undefined;

  return {
    id: row.id,
    name: row.name,
    policyName: row.policy?.name ?? row.name,
    policyId: row.policy?.id ?? row.policy_id ?? null,
    nodeName: row.node?.name ?? `节点-${row.node_id ?? 0}`,
    nodeId: row.node?.id ?? row.node_id ?? 0,
    status,
    progress: deriveTaskProgress(status, retryCount, index),
    startedAt: formatTime(row.last_run_at ?? row.created_at),
    nextRunAt: formatTime(row.next_run_at),
    errorCode,
    lastError: row.last_error ?? undefined,
    retryCount,
    command: row.command ?? undefined,
    rsyncSource: row.rsync_source ?? undefined,
    rsyncTarget: row.rsync_target ?? undefined,
    executorType: mapTaskExecutor(row.executor_type),
    cronSpec: row.cron_spec ?? undefined,
    updatedAt: formatTime(row.updated_at),
    speedMbps: 0
  };
}

function mapTaskLog(row: TaskLogResponse): LogEvent {
  return {
    id: `history-${row.task_id}-${row.id}`,
    logId: row.id,
    timestamp: formatTime(row.created_at),
    level: mapLogLevel(row.level),
    message: row.message,
    taskId: row.task_id,
    errorCode: extractErrorCode(row.message)
  };
}

function parseNumericId(rawId: string, prefix: string) {
  const value = rawId.trim();
  if (!value) {
    throw new Error(`无效的 ${prefix} ID：不能为空`);
  }

  if (value.startsWith(`${prefix}-`)) {
    const suffix = value.slice(prefix.length + 1);
    if (/^\d+$/.test(suffix)) {
      const parsed = Number.parseInt(suffix, 10);
      if (parsed > 0) {
        return parsed;
      }
    }
  } else if (/^\d+$/.test(value)) {
    const parsed = Number.parseInt(value, 10);
    if (parsed > 0) {
      return parsed;
    }
  }

  throw new Error(`无效的 ${prefix} ID：${rawId}（期望格式：${prefix}-123 或 123）`);
}

export const apiClient = {
  async login(username: string, password: string): Promise<LoginResponse> {
    const result = await request<LoginResponse>("/auth/login", {
      method: "POST",
      body: { username, password }
    });
    if (!result || typeof result !== "object" || !("token" in result)) {
      throw new ApiError(500, "登录响应格式异常", result);
    }
    return result;
  },

  async getNodes(token: string): Promise<NodeRecord[]> {
    const payload = await request<Envelope<NodeResponse[]>>("/nodes", { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row) => mapNode(row));
  },

  async createNode(token: string, input: NewNodeInput): Promise<NodeRecord> {
    const payload = await request<Envelope<NodeResponse>>("/nodes", {
      method: "POST",
      token,
      body: {
        name: input.name,
        host: input.host,
        port: input.port,
        username: input.username,
        auth_type: input.authType,
        password: input.password,
        ssh_key_id: input.keyId ? parseNumericId(input.keyId, "key") : null,
        private_key: input.inlinePrivateKey,
        key_type: input.inlineKeyType,
        tags: input.tags,
        base_path: input.basePath
      }
    });
    const row = unwrapData(payload);
    return mapNode(row);
  },

  async updateNode(token: string, nodeId: number, input: NewNodeInput): Promise<NodeRecord> {
    const payload = await request<Envelope<NodeResponse>>(`/nodes/${nodeId}`, {
      method: "PUT",
      token,
      body: {
        name: input.name,
        host: input.host,
        port: input.port,
        username: input.username,
        auth_type: input.authType,
        password: input.password,
        ssh_key_id: input.keyId ? parseNumericId(input.keyId, "key") : null,
        private_key: input.inlinePrivateKey,
        key_type: input.inlineKeyType,
        tags: input.tags,
        base_path: input.basePath
      }
    });
    const row = unwrapData(payload);
    return mapNode(row);
  },

  async deleteNode(token: string, nodeId: number) {
    await request(`/nodes/${nodeId}`, {
      method: "DELETE",
      token
    });
  },

  async deleteNodes(token: string, nodeIds: number[]): Promise<{ deleted: number; notFoundIds: number[] }> {
    const payload = await request<NodeBatchDeleteResponse>("/nodes/batch-delete", {
      method: "POST",
      token,
      body: {
        ids: nodeIds
      }
    });

    return {
      deleted: Number(payload.deleted ?? 0),
      notFoundIds: Array.isArray(payload.not_found_ids) ? payload.not_found_ids : []
    };
  },

  async testNodeConnection(token: string, nodeId: number): Promise<TestNodeResponse> {
    return request<TestNodeResponse>(`/nodes/${nodeId}/test-connection`, {
      method: "POST",
      token
    });
  },

  async getPolicies(token: string): Promise<PolicyRecord[]> {
    const payload = await request<Envelope<PolicyResponse[]>>("/policies", { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row) => mapPolicy(row));
  },

  async createPolicy(token: string, input: NewPolicyInput): Promise<PolicyRecord> {
    const payload = await request<Envelope<PolicyResponse>>("/policies", {
      method: "POST",
      token,
      body: {
        name: input.name,
        source_path: input.sourcePath,
        target_path: input.targetPath,
        cron_spec: input.cron,
        enabled: input.enabled
      }
    });
    return mapPolicy(unwrapData(payload));
  },

  async updatePolicy(token: string, policyId: number, input: NewPolicyInput): Promise<PolicyRecord> {
    const payload = await request<Envelope<PolicyResponse>>(`/policies/${policyId}`, {
      method: "PUT",
      token,
      body: {
        name: input.name,
        source_path: input.sourcePath,
        target_path: input.targetPath,
        cron_spec: input.cron,
        enabled: input.enabled
      }
    });
    return mapPolicy(unwrapData(payload));
  },

  async deletePolicy(token: string, policyId: number) {
    await request(`/policies/${policyId}`, {
      method: "DELETE",
      token
    });
  },

  async getTasks(token: string): Promise<TaskRecord[]> {
    const payload = await request<Envelope<TaskResponse[]>>("/tasks", { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row, index) => mapTask(row, index));
  },

  async getTask(token: string, taskId: number): Promise<TaskRecord> {
    const payload = await request<Envelope<TaskResponse>>(`/tasks/${taskId}`, { token });
    return mapTask(unwrapData(payload), 0);
  },

  async createTask(token: string, input: NewTaskInput): Promise<TaskRecord> {
    const payload = await request<Envelope<TaskResponse>>("/tasks", {
      method: "POST",
      token,
      body: {
        name: input.name,
        node_id: input.nodeId,
        policy_id: input.policyId ?? null,
        rsync_source: input.rsyncSource,
        rsync_target: input.rsyncTarget,
        executor_type: input.executorType,
        cron_spec: input.cronSpec
      }
    });
    return mapTask(unwrapData(payload), 0);
  },

  async updateTask(token: string, taskId: number, input: NewTaskInput): Promise<TaskRecord> {
    const payload = await request<Envelope<TaskResponse>>(`/tasks/${taskId}`, {
      method: "PUT",
      token,
      body: {
        name: input.name,
        node_id: input.nodeId,
        policy_id: input.policyId ?? null,
        rsync_source: input.rsyncSource,
        rsync_target: input.rsyncTarget,
        executor_type: input.executorType,
        cron_spec: input.cronSpec
      }
    });
    return mapTask(unwrapData(payload), 0);
  },

  async deleteTask(token: string, taskId: number) {
    await request(`/tasks/${taskId}`, {
      method: "DELETE",
      token
    });
  },

  async getTaskLogs(
    token: string,
    taskId: number,
    options?: {
      beforeId?: number;
      limit?: number;
      level?: "info" | "warn" | "error";
    }
  ): Promise<LogEvent[]> {
    const query = new URLSearchParams();
    if (options?.beforeId && Number.isFinite(options.beforeId) && options.beforeId > 0) {
      query.set("before_id", String(options.beforeId));
    }
    if (options?.limit && Number.isFinite(options.limit) && options.limit > 0) {
      query.set("limit", String(options.limit));
    }
    if (options?.level) {
      query.set("level", options.level);
    }
    const suffix = query.toString() ? `?${query.toString()}` : "";
    const payload = await request<Envelope<TaskLogResponse[]>>(`/tasks/${taskId}/logs${suffix}`, { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row) => mapTaskLog(row));
  },

  async getSSHKeys(token: string): Promise<SSHKeyRecord[]> {
    const payload = await request<Envelope<SSHKeyResponse[]>>("/ssh-keys", { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row) => mapSSHKey(row));
  },

  async createSSHKey(token: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
    const privateKey = input.privateKey.trim();
    const payload = await request<Envelope<SSHKeyResponse>>("/ssh-keys", {
      method: "POST",
      token,
      body: {
        name: input.name,
        username: input.username,
        key_type: input.keyType,
        private_key: privateKey
      }
    });
    return mapSSHKey(unwrapData(payload));
  },

  async updateSSHKey(token: string, keyId: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
    const numericId = parseNumericId(keyId, "key");
    const privateKey = input.privateKey.trim();
    const payload = await request<Envelope<SSHKeyResponse>>(`/ssh-keys/${numericId}`, {
      method: "PUT",
      token,
      body: {
        name: input.name,
        username: input.username,
        key_type: input.keyType,
        ...(privateKey ? { private_key: privateKey } : {})
      }
    });
    return mapSSHKey(unwrapData(payload));
  },

  async deleteSSHKey(token: string, keyId: string) {
    const numericId = parseNumericId(keyId, "key");
    await request(`/ssh-keys/${numericId}`, {
      method: "DELETE",
      token
    });
  },

  async getIntegrations(token: string): Promise<IntegrationChannel[]> {
    const payload = await request<Envelope<IntegrationResponse[]>>("/integrations", { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row) => mapIntegration(row));
  },

  async getAlerts(token: string): Promise<AlertRecord[]> {
    const payload = await request<Envelope<AlertResponse[]>>("/alerts", { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row) => mapAlert(row));
  },

  async getAlertDeliveries(token: string, alertId: string): Promise<AlertDeliveryRecord[]> {
    const numericId = parseNumericId(alertId, "alert");
    const payload = await request<Envelope<AlertDeliveryResponse[]>>(`/alerts/${numericId}/deliveries`, { token });
    const rows = unwrapData(payload) ?? [];
    return rows.map((row) => mapAlertDelivery(row));
  },

  async getAlertDeliveryStats(token: string, options?: { hours?: number }): Promise<AlertDeliveryStats> {
    const query = new URLSearchParams();
    if (options?.hours && Number.isFinite(options.hours) && options.hours > 0) {
      query.set("hours", String(Math.floor(options.hours)));
    }
    const suffix = query.toString() ? `?${query.toString()}` : "";
    const payload = await request<Envelope<DeliveryStatsResponse>>(`/alerts/delivery-stats${suffix}`, { token });
    return mapDeliveryStats(unwrapData(payload));
  },

  async getAuditLogs(
    token: string,
    options?: {
      username?: string;
      role?: string;
      method?: string;
      path?: string;
      statusCode?: number;
      from?: string;
      to?: string;
      limit?: number;
      offset?: number;
    }
  ): Promise<{ items: AuditLogRecord[]; total: number; limit: number; offset: number }> {
    const query = new URLSearchParams();
    if (options?.username?.trim()) {
      query.set("username", options.username.trim());
    }
    if (options?.role?.trim()) {
      query.set("role", options.role.trim());
    }
    if (options?.method?.trim()) {
      query.set("method", options.method.trim());
    }
    if (options?.path?.trim()) {
      query.set("path", options.path.trim());
    }
    if (options?.statusCode && Number.isFinite(options.statusCode)) {
      query.set("status_code", String(options.statusCode));
    }
    if (options?.from?.trim()) {
      query.set("from", options.from.trim());
    }
    if (options?.to?.trim()) {
      query.set("to", options.to.trim());
    }
    if (options?.limit && Number.isFinite(options.limit) && options.limit > 0) {
      query.set("limit", String(options.limit));
    }
    if (options?.offset && Number.isFinite(options.offset) && options.offset >= 0) {
      query.set("offset", String(options.offset));
    }

    const suffix = query.toString() ? `?${query.toString()}` : "";
    const payload = await request<Envelope<AuditLogResponse[]> & { total?: number; limit?: number; offset?: number }>(
      `/audit-logs${suffix}`,
      {
        token
      }
    );
    const rows = unwrapData(payload) ?? [];
    return {
      items: rows.map((row) => mapAuditLog(row)),
      total: typeof payload.total === "number" ? payload.total : rows.length,
      limit: typeof payload.limit === "number" ? payload.limit : rows.length,
      offset: typeof payload.offset === "number" ? payload.offset : 0
    };
  },

  async exportAuditLogsCSV(
    token: string,
    options?: {
      username?: string;
      role?: string;
      method?: string;
      path?: string;
      statusCode?: number;
      from?: string;
      to?: string;
      limit?: number;
    }
  ): Promise<Blob> {
    const query = new URLSearchParams();
    if (options?.username?.trim()) {
      query.set("username", options.username.trim());
    }
    if (options?.role?.trim()) {
      query.set("role", options.role.trim());
    }
    if (options?.method?.trim()) {
      query.set("method", options.method.trim());
    }
    if (options?.path?.trim()) {
      query.set("path", options.path.trim());
    }
    if (options?.statusCode && Number.isFinite(options.statusCode)) {
      query.set("status_code", String(options.statusCode));
    }
    if (options?.from?.trim()) {
      query.set("from", options.from.trim());
    }
    if (options?.to?.trim()) {
      query.set("to", options.to.trim());
    }
    if (options?.limit && Number.isFinite(options.limit) && options.limit > 0) {
      query.set("limit", String(options.limit));
    }

    const suffix = query.toString() ? `?${query.toString()}` : "";
    const response = await fetchWithFallback(`/audit-logs/export${suffix}`, {
      method: "GET",
      headers: {
        Authorization: `Bearer ${token}`
      }
    });

    if (!response.ok) {
      const text = await response.text();
      let detail: unknown = text;
      if (text) {
        try {
          detail = JSON.parse(text);
        } catch {
          detail = text;
        }
      }
      throw new ApiError(response.status, `请求失败：${response.status}`, detail);
    }

    return response.blob();
  },

  async ackAlert(token: string, alertId: string): Promise<AlertRecord> {
    const numericId = parseNumericId(alertId, "alert");
    const payload = await request<Envelope<AlertResponse>>(`/alerts/${numericId}/ack`, {
      method: "POST",
      token
    });
    return mapAlert(unwrapData(payload));
  },

  async resolveAlert(token: string, alertId: string): Promise<AlertRecord> {
    const numericId = parseNumericId(alertId, "alert");
    const payload = await request<Envelope<AlertResponse>>(`/alerts/${numericId}/resolve`, {
      method: "POST",
      token
    });
    return mapAlert(unwrapData(payload));
  },

  async retryAlertDelivery(token: string, alertId: string, integrationId: string): Promise<AlertDeliveryRetryResult> {
    const numericAlertID = parseNumericId(alertId, "alert");
    const numericIntegrationID = parseNumericId(integrationId, "int");
    const payload = await request<Envelope<RetryAlertDeliveryResponse>>(`/alerts/${numericAlertID}/retry-delivery`, {
      method: "POST",
      token,
      body: {
        integration_id: numericIntegrationID
      }
    });
    const data = unwrapData(payload);
    return {
      ok: Boolean(data?.ok),
      message: data?.message ?? "重发完成",
      delivery: mapAlertDelivery(data.delivery)
    };
  },

  async retryFailedDeliveries(token: string, alertId: string): Promise<AlertBulkRetryResult> {
    const numericAlertID = parseNumericId(alertId, "alert");
    const payload = await request<Envelope<RetryFailedDeliveriesResponse>>(`/alerts/${numericAlertID}/retry-failed-deliveries`, {
      method: "POST",
      token
    });
    const data = unwrapData(payload);
    return {
      ok: Boolean(data?.ok),
      message: data?.message ?? "批量重发完成",
      totalFailed: Number(data?.total_failed ?? 0),
      successCount: Number(data?.success_count ?? 0),
      failedCount: Number(data?.failed_count ?? 0),
      newDeliveries: Array.isArray(data?.new_deliveries)
        ? data.new_deliveries.map((one) => mapAlertDelivery(one))
        : []
    };
  },

  async createIntegration(token: string, input: NewIntegrationInput): Promise<IntegrationChannel> {
    const payload = await request<Envelope<IntegrationResponse>>("/integrations", {
      method: "POST",
      token,
      body: {
        type: input.type,
        name: input.name,
        endpoint: input.endpoint,
        enabled: input.enabled,
        fail_threshold: input.failThreshold,
        cooldown_minutes: input.cooldownMinutes
      }
    });
    return mapIntegration(unwrapData(payload));
  },

  async updateIntegration(
    token: string,
    integrationId: string,
    patch: Partial<IntegrationChannel>
  ): Promise<IntegrationChannel> {
    const numericId = parseNumericId(integrationId, "int");
    const payload = await request<Envelope<IntegrationResponse>>(`/integrations/${numericId}`, {
      method: "PUT",
      token,
      body: {
        type: patch.type,
        name: patch.name,
        endpoint: patch.endpoint,
        enabled: patch.enabled,
        fail_threshold: patch.failThreshold,
        cooldown_minutes: patch.cooldownMinutes
      }
    });
    return mapIntegration(unwrapData(payload));
  },

  async testIntegration(token: string, integrationId: string): Promise<IntegrationProbeResult> {
    const numericId = parseNumericId(integrationId, "int");
    const payload = await request<Envelope<IntegrationTestResponse>>(`/integrations/${numericId}/test`, {
      method: "POST",
      token
    });
    const data = unwrapData(payload);
    return {
      ok: Boolean(data?.ok),
      message: data?.message ?? "测试完成",
      latencyMs: Number(data?.latency_ms ?? 0)
    };
  },

  async deleteIntegration(token: string, integrationId: string) {
    const numericId = parseNumericId(integrationId, "int");
    await request(`/integrations/${numericId}`, {
      method: "DELETE",
      token
    });
  },

  async triggerTask(token: string, taskId: number) {
    await request(`/tasks/${taskId}/trigger`, {
      method: "POST",
      token
    });
  },

  async cancelTask(token: string, taskId: number) {
    await request(`/tasks/${taskId}/cancel`, {
      method: "POST",
      token
    });
  }
};
