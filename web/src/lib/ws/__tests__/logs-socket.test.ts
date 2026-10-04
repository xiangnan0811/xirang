import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LogsSocketClient } from "@/lib/ws/logs-socket";

/**
 * 与 reconnecting-socket.test.ts 相同的同步 WebSocket fake。
 * close() 会立刻调用当前 onclose；CONNECTING 上的主动 close 要等 open 才真正关闭。
 */
class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  static instances: FakeWebSocket[] = [];

  url: string;
  protocols?: string | string[];
  readyState: number = FakeWebSocket.CONNECTING;
  binaryType: BinaryType = "blob";

  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;

  sent: Array<string | ArrayBuffer | ArrayBufferView | Blob> = [];

  constructor(url: string, protocols?: string | string[]) {
    this.url = url;
    this.protocols = protocols;
    FakeWebSocket.instances.push(this);
  }

  send(data: string | ArrayBuffer | ArrayBufferView | Blob): void {
    this.sent.push(data);
  }

  close(code: number = 1000, reason: string = ""): void {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code, reason, wasClean: code === 1000 } as CloseEvent);
  }

  fireOpen(): void {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.(new Event("open"));
  }

  fireMessage(data: unknown): void {
    this.onmessage?.({ data } as MessageEvent);
  }

  fireClose(code: number = 1006, reason: string = ""): void {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code, reason, wasClean: false } as CloseEvent);
  }

  fireError(): void {
    this.onerror?.(new Event("error"));
  }

  static reset(): void {
    FakeWebSocket.instances = [];
  }
}

const originalWebSocket = globalThis.WebSocket;
const DIRECT_LOG_SOCKET = "ws://127.0.0.1:8080/api/v1/ws/logs";
// 首次退避为 baseDelay(2500) * [0.5, 1)，心跳间隔 25000，3000 只推进这一次重连。
const FIRST_RETRY_MS = 3_000;

function installWebSocket(value: unknown) {
  Object.defineProperty(globalThis, "WebSocket", {
    configurable: true,
    writable: true,
    value,
  });
}

function primaryLogSocket(): string {
  const protocol = window.location.protocol === "https:" ? "wss" : "ws";
  return `${protocol}://${window.location.host}/api/v1/ws/logs`;
}

function logFrame(logId: number, message: string): string {
  return JSON.stringify({
    log_id: logId,
    message,
    level: "info",
    timestamp: "2026-10-04T00:00:00.000Z",
  });
}

describe("LogsSocketClient", () => {
  let client: LogsSocketClient | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubEnv("VITE_WS_URL", "");
    vi.stubEnv("VITE_API_BASE_URL", "/api/v1");
    vi.stubEnv("VITE_DEV_API_DIRECT_URL", "http://127.0.0.1:8080/api/v1");
    FakeWebSocket.reset();
    installWebSocket(FakeWebSocket);
  });

  afterEach(() => {
    client?.disconnect();
    client = undefined;
    vi.useRealTimers();
    vi.unstubAllEnvs();
    installWebSocket(originalWebSocket);
  });

  it("ignores a replaced socket late close and error after the new socket is open", () => {
    expect(import.meta.env.DEV).toBe(true);
    const primary = primaryLogSocket();
    expect(DIRECT_LOG_SOCKET).not.toBe(primary);

    const statuses: boolean[] = [];
    const messages: string[] = [];
    client = new LogsSocketClient();
    client.onStatusChange((connected) => {
      statuses.push(connected);
    });
    client.subscribe((event) => {
      messages.push(event.message);
    });

    client.connect("token-a");
    const replaced = FakeWebSocket.instances[0]!;
    expect(replaced.url).toBe(primary);
    expect(replaced.readyState).toBe(FakeWebSocket.CONNECTING);

    client.disconnect();
    expect(statuses).toEqual([false]);

    client.connect("token-b");
    const current = FakeWebSocket.instances[1]!;
    expect(current.url).toBe(primary);
    current.fireOpen();
    expect(statuses).toEqual([false, true]);
    expect(current.sent).toEqual([JSON.stringify({ type: "auth", token: "token-b" })]);

    current.fireMessage(logFrame(42, "current"));
    replaced.fireOpen();
    replaced.fireError();
    replaced.fireMessage(logFrame(7, "stale"));
    current.fireMessage(logFrame(43, "still-current"));

    expect(statuses).toEqual([false, true]);
    expect(messages).toEqual(["current", "still-current"]);

    current.fireClose(1006);
    expect(statuses).toEqual([false, true, false]);
    expect(FakeWebSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(FIRST_RETRY_MS);
    expect(FakeWebSocket.instances).toHaveLength(3);
    expect(FakeWebSocket.instances[2]!.url).toBe(DIRECT_LOG_SOCKET);
  });

  it("emits the active socket message and reports disconnect for that socket only", () => {
    const primary = primaryLogSocket();
    const statuses: boolean[] = [];
    const messages: string[] = [];
    client = new LogsSocketClient();
    client.onStatusChange((connected) => {
      statuses.push(connected);
    });
    client.subscribe((event) => {
      messages.push(event.message);
    });

    client.connect("first");
    const replaced = FakeWebSocket.instances[0]!;
    replaced.fireOpen();
    client.connect("second");
    const current = FakeWebSocket.instances[1]!;
    expect(current.url).toBe(primary);
    current.fireOpen();
    expect(statuses).toEqual([true, true]);

    replaced.fireClose(1006);
    replaced.fireError();
    replaced.fireMessage(logFrame(7, "stale"));
    current.fireMessage(logFrame(42, "current"));
    expect(messages).toEqual(["current"]);
    expect(statuses).toEqual([true, true]);

    client.disconnect();
    expect(statuses).toEqual([true, true, false]);
    expect(FakeWebSocket.instances).toHaveLength(2);
    current.fireClose(1006);
    current.fireError();
    current.fireMessage(logFrame(99, "after-close"));
    expect(statuses).toEqual([true, true, false]);
    expect(messages).toEqual(["current"]);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("reports disconnected when the active socket errors", () => {
    const statuses: boolean[] = [];
    client = new LogsSocketClient();
    client.onStatusChange((connected) => {
      statuses.push(connected);
    });
    client.connect("token");
    const current = FakeWebSocket.instances[0]!;
    current.fireOpen();
    current.fireError();
    expect(statuses).toEqual([true, false]);
  });
});
