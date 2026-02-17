import type { LogEvent } from "@/types/domain";

type MessageListener = (event: LogEvent) => void;
type StatusListener = (connected: boolean) => void;

export type LogsSocketConnectOptions = {
  taskId?: number;
  sinceId?: number;
};

const RETRY_DELAY_MS = 2500;
const DEFAULT_DEV_DIRECT_API = "http://127.0.0.1:8080/api/v1";
const WS_AUTH_PROTOCOL = "xirang-auth.v1";
const WS_AUTH_TOKEN_PREFIX = "xirang-auth-token.";

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
  const nodeName = typeof payload.node_name === "string" ? payload.node_name : undefined;
  const ts = typeof payload.timestamp === "string" ? payload.timestamp : new Date().toISOString();
  const levelRaw = payload.level;
  const level = levelRaw === "warn" || levelRaw === "error" ? levelRaw : "info";
  const message = typeof payload.message === "string" ? payload.message : "";

  return {
    id: logID ? `live-${logID}` : `${taskID ?? "global"}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    logId: logID,
    timestamp: new Date(ts).toLocaleString(),
    level,
    message,
    taskId: taskID,
    nodeName
  };
}

export class LogsSocketClient {
  private socket: WebSocket | null = null;
  private reconnectTimer: number | null = null;
  private token = "";
  private manuallyClosed = false;
  private taskId: number | undefined;
  private sinceId: number | undefined;
  private activeCandidateIndex = 0;
  private connectAttempt = 0;
  private readonly listeners = new Set<MessageListener>();
  private readonly statusListeners = new Set<StatusListener>();
  private readonly wsCandidates: string[];

  constructor() {
    this.wsCandidates = buildWsCandidates();
  }

  connect(token: string, options?: LogsSocketConnectOptions) {
    this.token = token;
    this.taskId = options?.taskId;
    this.sinceId = options?.sinceId;
    this.manuallyClosed = false;
    this.activeCandidateIndex = 0;
    this.open();
  }

  updateSinceId(sinceId: number | undefined) {
    if (!sinceId || !Number.isFinite(sinceId)) {
      return;
    }
    this.sinceId = Math.max(this.sinceId ?? 0, sinceId);
  }

  disconnect() {
    this.manuallyClosed = true;
    this.clearReconnectTimer();

    if (this.socket) {
      const current = this.socket;
      this.socket = null;

      if (current.readyState === WebSocket.OPEN) {
        current.close(1000, "manual-close");
      } else if (current.readyState === WebSocket.CONNECTING) {
        current.onopen = () => {
          current.close(1000, "manual-close");
        };
      }
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

  private buildRequestUrl() {
    const base = this.wsCandidates[this.activeCandidateIndex] ?? this.wsCandidates[0] ?? toWebSocketUrl("/api/v1");
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

  private buildAuthProtocols() {
    if (!this.token) {
      return undefined;
    }
    return [WS_AUTH_PROTOCOL, `${WS_AUTH_TOKEN_PREFIX}${this.token}`];
  }

  private open() {
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return;
    }

    const attempt = ++this.connectAttempt;
    const protocols = this.buildAuthProtocols();
    const socket = protocols ? new WebSocket(this.buildRequestUrl(), protocols) : new WebSocket(this.buildRequestUrl());
    this.socket = socket;

    socket.onopen = () => {
      if (attempt !== this.connectAttempt || this.manuallyClosed) {
        socket.close(1000, "stale-open");
        return;
      }
      this.emitStatus(true);
      this.clearReconnectTimer();
      this.activeCandidateIndex = 0;
    };

    socket.onmessage = (event) => {
      try {
        const parsed = normalizeIncoming(JSON.parse(event.data));
        if (parsed) {
          if (parsed.logId) {
            this.updateSinceId(parsed.logId);
          }
          this.listeners.forEach((listener) => listener(parsed));
        }
      } catch {
        // 忽略非法消息
      }
    };

    socket.onclose = () => {
      if (this.socket === socket) {
        this.socket = null;
      }
      this.emitStatus(false);
      if (!this.manuallyClosed) {
        this.tryNextCandidateOrReconnect();
      }
    };

    socket.onerror = () => {
      this.emitStatus(false);
    };
  }

  private tryNextCandidateOrReconnect() {
    if (this.activeCandidateIndex < this.wsCandidates.length - 1) {
      this.activeCandidateIndex += 1;
      this.open();
      return;
    }
    this.activeCandidateIndex = 0;
    this.scheduleReconnect();
  }

  private scheduleReconnect() {
    this.clearReconnectTimer();
    this.reconnectTimer = window.setTimeout(() => this.open(), RETRY_DELAY_MS);
  }

  private clearReconnectTimer() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  private emitStatus(connected: boolean) {
    this.statusListeners.forEach((listener) => listener(connected));
  }
}
