/**
 * 通用 WebSocket 重连/心跳/token 刷新抽象层。
 *
 * 设计目标：
 * - 提取自 `logs-socket.ts` 已经稳定的 D8 行为（指数退避 + jitter + heartbeat + 标签页可见性恢复）
 * - 不耦合任何业务消息体（不解析 JSON，不做 schema 校验）
 * - URL 支持回调形式以保留 logs-socket 的 candidate URL 切换能力
 * - 收到自定义关闭码 4401 时调用 `onTokenRefreshNeeded` 刷新 token 后再重连
 *
 * 协议固有限制：
 * - 重连后服务端 session 已失效（如 SSH PTY），调用方需要在 `onReconnect`
 *   回调中重置上层状态（如清屏、提示用户重新登录）。
 */
export type SocketUrl = string | (() => string);

export type ReconnectingSocketOptions = {
  /** 连接 URL；支持回调形式以便在重连时切换不同的 URL（candidate fallback） */
  url: SocketUrl;
  /** 子协议（透传给原生 WebSocket 构造函数） */
  protocols?: string | string[];
  /** binaryType（"arraybuffer" 适合二进制 PTY 流） */
  binaryType?: BinaryType;

  /** 指数退避初始毫秒，默认 2500 */
  baseDelayMs?: number;
  /** 指数退避上限毫秒，默认 30_000 */
  maxDelayMs?: number;
  /** 最大重试次数（不含首次连接），默认 20 */
  maxRetries?: number;
  /** 是否在退避之上叠加 [0.5, 1.0) 的随机系数，默认 true */
  jitter?: boolean;

  /** 心跳间隔毫秒，默认 25_000；设为 0 禁用心跳 */
  heartbeatIntervalMs?: number;
  /** 心跳超时毫秒（无 pong 则视为死连接），默认 60_000；仅在提供 isPongMessage 时启用 */
  heartbeatTimeoutMs?: number;
  /** 心跳 ping 消息生成器；默认不发 ping（仅依赖 onclose 触发重连） */
  heartbeatPing?: () => string | ArrayBuffer | ArrayBufferView;
  /** 判定一条消息是否为 pong 响应（用于刷新心跳超时计时器） */
  isPongMessage?: (data: unknown) => boolean;

  /** 收到关闭码 4401 时调用，用于刷新 token；返回 promise，resolve 后才会重连 */
  onTokenRefreshNeeded?: () => void | Promise<void>;

  /** 连接打开后的回调（每次 open 都会触发，包括 reconnect 后） */
  onOpen?: (socket: WebSocket) => void;
  /** 收到任何消息时回调（pong 也会触发；调用方自行决定是否过滤） */
  onMessage?: (event: MessageEvent) => void;
  /**
   * 重连成功（第二次及之后的 open）触发；attempt 是已用掉的 retry 计数。
   * 用于 web-terminal 这种重连后需要重置 UI / 提示用户重新登录的场景。
   */
  onReconnect?: (attempt: number) => void;
  /** 关闭事件透传（包括正常关闭与异常断开） */
  onClose?: (event: CloseEvent) => void;
  /** error 事件透传 */
  onError?: (event: Event) => void;
  /** 达到 maxRetries 后停止重连时触发，调用方可显示永久错误 */
  onGiveUp?: () => void;
};

const DEFAULT_BASE_DELAY = 2500;
const DEFAULT_MAX_DELAY = 30_000;
const DEFAULT_MAX_RETRIES = 20;
const DEFAULT_HEARTBEAT_INTERVAL = 25_000;
const DEFAULT_HEARTBEAT_TIMEOUT = 60_000;

/** 关闭码 4401：服务端要求 token 刷新；触发 onTokenRefreshNeeded 后再重连。 */
export const TOKEN_REFRESH_CLOSE_CODE = 4401;

export class ReconnectingSocket {
  private socket: WebSocket | null = null;
  private reconnectTimer: number | null = null;
  private heartbeatTimer: number | null = null;
  private heartbeatTimeoutTimer: number | null = null;
  private retries = 0;
  private connectAttempt = 0;
  private manuallyClosed = false;
  private gaveUp = false;
  private visibilityHandler: (() => void) | null = null;
  private readonly opts: Required<
    Omit<
      ReconnectingSocketOptions,
      | "protocols"
      | "binaryType"
      | "heartbeatPing"
      | "isPongMessage"
      | "onTokenRefreshNeeded"
      | "onOpen"
      | "onMessage"
      | "onReconnect"
      | "onClose"
      | "onError"
      | "onGiveUp"
    >
  > &
    Pick<
      ReconnectingSocketOptions,
      | "protocols"
      | "binaryType"
      | "heartbeatPing"
      | "isPongMessage"
      | "onTokenRefreshNeeded"
      | "onOpen"
      | "onMessage"
      | "onReconnect"
      | "onClose"
      | "onError"
      | "onGiveUp"
    >;

  constructor(options: ReconnectingSocketOptions) {
    this.opts = {
      url: options.url,
      protocols: options.protocols,
      binaryType: options.binaryType,
      baseDelayMs: options.baseDelayMs ?? DEFAULT_BASE_DELAY,
      maxDelayMs: options.maxDelayMs ?? DEFAULT_MAX_DELAY,
      maxRetries: options.maxRetries ?? DEFAULT_MAX_RETRIES,
      jitter: options.jitter ?? true,
      heartbeatIntervalMs: options.heartbeatIntervalMs ?? DEFAULT_HEARTBEAT_INTERVAL,
      heartbeatTimeoutMs: options.heartbeatTimeoutMs ?? DEFAULT_HEARTBEAT_TIMEOUT,
      heartbeatPing: options.heartbeatPing,
      isPongMessage: options.isPongMessage,
      onTokenRefreshNeeded: options.onTokenRefreshNeeded,
      onOpen: options.onOpen,
      onMessage: options.onMessage,
      onReconnect: options.onReconnect,
      onClose: options.onClose,
      onError: options.onError,
      onGiveUp: options.onGiveUp,
    };
  }

  /** 启动连接（仅首次调用有效；后续会自动重连） */
  connect(): void {
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return;
    }
    this.manuallyClosed = false;
    this.gaveUp = false;
    this.retries = 0;
    this.addVisibilityListener();
    this.open();
  }

  /** 发送数据（连接未打开时返回 false） */
  send(data: string | ArrayBufferLike | ArrayBufferView | Blob): boolean {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return false;
    }
    try {
      // WebSocket.send 的多个重载在 lib.dom.d.ts 中是分别声明的；这里 narrow 一下
      this.socket.send(data as never);
      return true;
    } catch {
      return false;
    }
  }

  /** 主动关闭，关闭后不会再尝试重连 */
  close(code = 1000, reason = "manual-close"): void {
    this.manuallyClosed = true;
    this.clearReconnectTimer();
    this.stopHeartbeat();
    this.removeVisibilityListener();
    if (!this.socket) return;

    const current = this.socket;
    this.socket = null;
    if (current.readyState === WebSocket.OPEN) {
      current.close(code, reason);
    } else if (current.readyState === WebSocket.CONNECTING) {
      current.onopen = () => current.close(code, reason);
    }
  }

  /** 是否已达最大重试次数（不再自动重连） */
  isGivingUp(): boolean {
    return this.gaveUp;
  }

  get readyState(): number {
    return this.socket?.readyState ?? WebSocket.CLOSED;
  }

  private resolveUrl(): string {
    return typeof this.opts.url === "function" ? this.opts.url() : this.opts.url;
  }

  private open(): void {
    if (this.manuallyClosed) return;
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return;
    }

    const attempt = ++this.connectAttempt;
    const url = this.resolveUrl();
    const socket = this.opts.protocols !== undefined
      ? new WebSocket(url, this.opts.protocols)
      : new WebSocket(url);
    if (this.opts.binaryType) {
      socket.binaryType = this.opts.binaryType;
    }
    this.socket = socket;
    const wasReconnect = this.retries > 0;
    const reconnectAttemptCount = this.retries;

    socket.onopen = () => {
      if (attempt !== this.connectAttempt || this.manuallyClosed) {
        socket.close(1000, "stale-open");
        return;
      }
      this.clearReconnectTimer();
      this.retries = 0;
      this.gaveUp = false;
      this.startHeartbeat();
      this.opts.onOpen?.(socket);
      if (wasReconnect) {
        this.opts.onReconnect?.(reconnectAttemptCount);
      }
    };

    socket.onmessage = (event) => {
      if (attempt !== this.connectAttempt) return;
      // 心跳 pong：刷新超时计时器
      if (this.opts.isPongMessage) {
        try {
          const parsed = typeof event.data === "string" ? safeJsonParse(event.data) : event.data;
          if (this.opts.isPongMessage(parsed)) {
            this.refreshHeartbeatTimeout();
          }
        } catch {
          // ignore
        }
      }
      this.opts.onMessage?.(event);
    };

    socket.onclose = (event) => {
      if (this.socket === socket) {
        this.socket = null;
      }
      this.stopHeartbeat();
      // 忽略已被新连接取代的旧 socket（React 18 Strict Mode 双挂载场景）
      if (attempt !== this.connectAttempt) return;
      this.opts.onClose?.(event);

      if (this.manuallyClosed) return;

      if (event.code === TOKEN_REFRESH_CLOSE_CODE && this.opts.onTokenRefreshNeeded) {
        this.handleTokenRefresh();
        return;
      }

      this.scheduleReconnect();
    };

    socket.onerror = (event) => {
      if (attempt !== this.connectAttempt) return;
      this.opts.onError?.(event);
    };
  }

  private async handleTokenRefresh(): Promise<void> {
    try {
      await this.opts.onTokenRefreshNeeded?.();
    } catch {
      // 刷新失败也走普通重连流程；调用方应在 onTokenRefreshNeeded 内自己处理
    }
    if (this.manuallyClosed) return;
    // token 刷新视为成功事件，不消耗 retry 计数；立即重连
    this.open();
  }

  private scheduleReconnect(): void {
    if (this.retries >= this.opts.maxRetries) {
      this.gaveUp = true;
      this.opts.onGiveUp?.();
      return;
    }
    this.clearReconnectTimer();
    const exp = Math.min(
      this.opts.baseDelayMs * Math.pow(2, this.retries),
      this.opts.maxDelayMs
    );
    const delay = this.opts.jitter ? exp * (0.5 + Math.random() * 0.5) : exp;
    this.retries += 1;
    this.reconnectTimer = window.setTimeout(() => this.open(), delay);
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  private startHeartbeat(): void {
    this.stopHeartbeat();
    if (this.opts.heartbeatIntervalMs <= 0) return;
    if (!this.opts.heartbeatPing) return;

    this.heartbeatTimer = window.setInterval(() => {
      if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return;
      try {
        const payload = this.opts.heartbeatPing!();
        this.socket.send(payload as never);
      } catch {
        // send 失败会触发 onclose，无需额外处理
      }
    }, this.opts.heartbeatIntervalMs);

    if (this.opts.isPongMessage) {
      this.refreshHeartbeatTimeout();
    }
  }

  private refreshHeartbeatTimeout(): void {
    if (this.heartbeatTimeoutTimer !== null) {
      clearTimeout(this.heartbeatTimeoutTimer);
      this.heartbeatTimeoutTimer = null;
    }
    if (this.opts.heartbeatTimeoutMs <= 0) return;
    this.heartbeatTimeoutTimer = window.setTimeout(() => {
      // 超时未收到 pong：主动关闭以触发 reconnect 流程
      if (this.socket && this.socket.readyState === WebSocket.OPEN) {
        try {
          this.socket.close(4000, "heartbeat-timeout");
        } catch {
          // ignore
        }
      }
    }, this.opts.heartbeatTimeoutMs);
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer !== null) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
    if (this.heartbeatTimeoutTimer !== null) {
      clearTimeout(this.heartbeatTimeoutTimer);
      this.heartbeatTimeoutTimer = null;
    }
  }

  private addVisibilityListener(): void {
    if (typeof document === "undefined") return;
    this.removeVisibilityListener();
    this.visibilityHandler = () => {
      if (document.visibilityState !== "visible" || this.manuallyClosed) return;
      // 已放弃重连：标签页恢复时重置计数尝试一次
      if (this.gaveUp) {
        this.gaveUp = false;
        this.retries = 0;
        this.open();
        return;
      }
      // socket 已关闭/不存在 → 立即尝试（取消等待中的退避计时器）
      if (!this.socket || this.socket.readyState === WebSocket.CLOSED || this.socket.readyState === WebSocket.CLOSING) {
        this.clearReconnectTimer();
        this.open();
      }
    };
    document.addEventListener("visibilitychange", this.visibilityHandler);
  }

  private removeVisibilityListener(): void {
    if (typeof document === "undefined") return;
    if (this.visibilityHandler) {
      document.removeEventListener("visibilitychange", this.visibilityHandler);
      this.visibilityHandler = null;
    }
  }
}

function safeJsonParse(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}
