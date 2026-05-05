import { formatTime } from "@/lib/api/core";
import type { LogEvent } from "@/types/domain";
import { ReconnectingSocket } from "@/lib/ws/reconnecting-socket";

type MessageListener = (event: LogEvent) => void;
type StatusListener = (connected: boolean) => void;

export type LogsSocketConnectOptions = {
  taskId?: number;
  sinceId?: number;
  tokenGetter?: () => string | null;
};

const DEFAULT_DEV_DIRECT_API = "http://127.0.0.1:8080/api/v1";

function normalizePath(path: string): string {
  if (!path.startsWith("/")) {
    return `/${path}`;
  }
  return path;
}

function toWebSocketUrl(apiBaseUrl: string): string {
  if (apiBaseUrl.startsWith("http://")) {
    return `${apiBaseUrl.replace("http://", "ws://")}/ws/logs`;
  }
  if (apiBaseUrl.startsWith("https://")) {
    return `${apiBaseUrl.replace("https://", "wss://")}/ws/logs`;
  }

  const protocol = window.location.protocol === "https:" ? "wss" : "ws";
  const basePath = normalizePath(apiBaseUrl).replace(/\/+$/, "");
  return `${protocol}://${window.location.host}${basePath}/ws/logs`;
}

function uniqueUrls(candidates: string[]) {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const item of candidates) {
    const normalized = item.trim();
    if (!normalized || seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    result.push(normalized);
  }
  return result;
}

function buildWsCandidates(): string[] {
  const configured = import.meta.env.VITE_WS_URL?.trim();
  if (configured) {
    return [configured];
  }

  const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? "/api/v1").trim();
  const candidates = [toWebSocketUrl(baseUrl)];

  if (import.meta.env.DEV && baseUrl.startsWith("/")) {
    const directApi = (import.meta.env.VITE_DEV_API_DIRECT_URL ?? DEFAULT_DEV_DIRECT_API).trim();
    candidates.push(toWebSocketUrl(directApi));
  }

  return uniqueUrls(candidates);
}

function normalizeIncoming(raw: unknown): LogEvent | null {
  if (!raw || typeof raw !== "object") {
    return null;
  }
  const payload = raw as Record<string, unknown>;
  const logID = typeof payload.log_id === "number"
    ? payload.log_id
    : typeof payload.id === "number"
      ? payload.id
      : undefined;
  const taskID = typeof payload.task_id === "number" ? payload.task_id : undefined;
  const taskRunID = typeof payload.task_run_id === "number" ? payload.task_run_id : undefined;
  const nodeName = typeof payload.node_name === "string" ? payload.node_name : undefined;
  const ts = typeof payload.timestamp === "string" ? payload.timestamp : new Date().toISOString();
  const levelRaw = payload.level;
  const level = levelRaw === "warn" || levelRaw === "error" ? levelRaw : "info";
  const message = typeof payload.message === "string" ? payload.message : "";
  const statusRaw = payload.status;
  const status =
    statusRaw === "pending" ||
    statusRaw === "running" ||
    statusRaw === "retrying" ||
    statusRaw === "failed" ||
    statusRaw === "success" ||
    statusRaw === "canceled"
      ? statusRaw
      : undefined;

  return {
    id: logID ? `live-${logID}` : `${taskID ?? "global"}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    logId: logID,
    timestamp: formatTime(ts),
    timestampMs: new Date(ts).getTime(),
    level,
    message,
    taskId: taskID,
    taskRunId: taskRunID,
    nodeName,
    status
  };
}

export class LogsSocketClient {
  private socket: ReconnectingSocket | null = null;
  private token = "";
  private tokenGetter: (() => string | null) | null = null;
  private taskId: number | undefined;
  private sinceId: number | undefined;
  private candidateIndex = 0;
  private readonly listeners = new Set<MessageListener>();
  private readonly statusListeners = new Set<StatusListener>();
  private readonly wsCandidates: string[];

  constructor() {
    this.wsCandidates = buildWsCandidates();
  }

  connect(token: string, options?: LogsSocketConnectOptions) {
    this.token = token;
    this.tokenGetter = options?.tokenGetter ?? null;
    this.taskId = options?.taskId;
    this.sinceId = options?.sinceId;
    this.candidateIndex = 0;

    if (this.socket) {
      this.socket.close(1000, "reconnect-with-new-token");
      this.socket = null;
    }

    this.socket = new ReconnectingSocket({
      url: () => this.buildRequestUrl(),
      // logs 使用 JSON 文本协议：发送 {type:"ping"} 心跳
      heartbeatPing: () => JSON.stringify({ type: "ping" }),
      onOpen: (ws) => {
        // 连接建立后立即用最新 token 发送 auth 消息
        ws.send(JSON.stringify({ type: "auth", token: this.currentToken() }));
        // 重置 candidate 索引：握手成功表示当前候选可用
        this.candidateIndex = 0;
        this.emitStatus(true);
      },
      onMessage: (event) => {
        try {
          const parsed = normalizeIncoming(JSON.parse(event.data));
          if (parsed) {
            this.listeners.forEach((listener) => listener(parsed));
          }
        } catch {
          // 忽略非法消息（如 pong 字符串等）
        }
      },
      onClose: () => {
        this.emitStatus(false);
        // 当前 candidate 失败 → 切换到下一个，下次 open 时由 url callback 选中
        if (this.wsCandidates.length > 1) {
          this.candidateIndex = (this.candidateIndex + 1) % this.wsCandidates.length;
        }
      },
      onError: () => {
        this.emitStatus(false);
      },
      onGiveUp: () => {
        this.emitStatus(false);
      },
    });

    this.socket.connect();
  }

  updateSinceId(sinceId: number | undefined) {
    if (!sinceId || !Number.isFinite(sinceId)) {
      return;
    }
    this.sinceId = Math.max(this.sinceId ?? 0, sinceId);
  }

  disconnect() {
    if (this.socket) {
      this.socket.close(1000, "manual-close");
      this.socket = null;
    }
    this.emitStatus(false);
  }

  subscribe(listener: MessageListener) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  onStatusChange(listener: StatusListener) {
    this.statusListeners.add(listener);
    return () => this.statusListeners.delete(listener);
  }

  /** 是否已达最大重试次数（不再自动重连） */
  isGivingUp(): boolean {
    return this.socket?.isGivingUp() ?? false;
  }

  private buildRequestUrl() {
    const base =
      this.wsCandidates[this.candidateIndex] ??
      this.wsCandidates[0] ??
      toWebSocketUrl("/api/v1");
    const search = new URLSearchParams();
    if (this.taskId && Number.isFinite(this.taskId)) {
      search.set("task_id", String(this.taskId));
    }
    if (this.sinceId && Number.isFinite(this.sinceId)) {
      search.set("since_id", String(this.sinceId));
    }
    const query = search.toString();
    return query ? `${base}?${query}` : base;
  }

  /** 获取最新 token：优先调用 getter，fallback 到存储的 token */
  private currentToken(): string {
    if (this.tokenGetter) {
      const fresh = this.tokenGetter();
      if (fresh) {
        this.token = fresh;
      }
    }
    return this.token;
  }

  private emitStatus(connected: boolean) {
    this.statusListeners.forEach((listener) => listener(connected));
  }
}
