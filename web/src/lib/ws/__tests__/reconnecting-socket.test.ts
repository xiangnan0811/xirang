import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ReconnectingSocket,
  TOKEN_REFRESH_CLOSE_CODE,
} from "@/lib/ws/reconnecting-socket";

/**
 * 简化的 WebSocket stub：让测试可以同步控制 open/close/message 事件。
 * 避免引入额外依赖（mock-socket 等），减小测试维护面。
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

  // helpers for tests
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

describe("ReconnectingSocket", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeWebSocket.reset();
    Object.assign(globalThis, { WebSocket: FakeWebSocket });
  });

  afterEach(() => {
    vi.useRealTimers();
    Object.assign(globalThis, { WebSocket: originalWebSocket });
  });

  it("connects and forwards messages via onMessage", () => {
    const onMessage = vi.fn();
    const onOpen = vi.fn();
    const sock = new ReconnectingSocket({
      url: "ws://test/echo",
      onOpen,
      onMessage,
    });
    sock.connect();

    expect(FakeWebSocket.instances).toHaveLength(1);
    const ws = FakeWebSocket.instances[0]!;
    expect(ws.url).toBe("ws://test/echo");

    ws.fireOpen();
    expect(onOpen).toHaveBeenCalledTimes(1);

    ws.fireMessage("hello");
    expect(onMessage).toHaveBeenCalledTimes(1);
    expect((onMessage.mock.calls[0]![0] as MessageEvent).data).toBe("hello");
  });

  it("auto-reconnects on abnormal close with exponential backoff", () => {
    const onReconnect = vi.fn();
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      baseDelayMs: 100,
      maxDelayMs: 10_000,
      maxRetries: 3,
      jitter: false,
      onReconnect,
    });
    sock.connect();
    const first = FakeWebSocket.instances[0]!;
    first.fireOpen();

    // Abnormal close → schedule reconnect at baseDelay (100ms)
    first.fireClose(1006);
    expect(FakeWebSocket.instances).toHaveLength(1);
    vi.advanceTimersByTime(100);
    expect(FakeWebSocket.instances).toHaveLength(2);

    // Open second connection → onReconnect should fire
    const second = FakeWebSocket.instances[1]!;
    second.fireOpen();
    expect(onReconnect).toHaveBeenCalledTimes(1);
    expect(onReconnect).toHaveBeenCalledWith(1);

    // Second close (without firing open this time) → backoff resets after success
    // so this is again the first retry from retries=0 → 100ms baseDelay
    second.fireClose(1006);
    vi.advanceTimersByTime(99);
    expect(FakeWebSocket.instances).toHaveLength(2); // not yet
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances).toHaveLength(3);
  });

  it("doubles backoff between consecutive failed attempts", () => {
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      baseDelayMs: 100,
      maxDelayMs: 10_000,
      maxRetries: 5,
      jitter: false,
    });
    sock.connect();

    // First attempt fails before opening → retry after 100ms
    FakeWebSocket.instances[0]!.fireClose(1006);
    vi.advanceTimersByTime(100);
    expect(FakeWebSocket.instances).toHaveLength(2);

    // Second attempt fails before opening → retry after 200ms
    FakeWebSocket.instances[1]!.fireClose(1006);
    vi.advanceTimersByTime(199);
    expect(FakeWebSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances).toHaveLength(3);
  });

  it("stops reconnecting after maxRetries and calls onGiveUp", () => {
    const onGiveUp = vi.fn();
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      baseDelayMs: 50,
      maxRetries: 2,
      jitter: false,
      onGiveUp,
    });
    sock.connect();

    // Initial attempt fails
    FakeWebSocket.instances[0]!.fireClose(1006);
    vi.advanceTimersByTime(50);
    // 1st retry fails
    FakeWebSocket.instances[1]!.fireClose(1006);
    vi.advanceTimersByTime(100);
    // 2nd retry fails → maxRetries hit
    FakeWebSocket.instances[2]!.fireClose(1006);

    expect(onGiveUp).toHaveBeenCalledTimes(1);
    expect(sock.isGivingUp()).toBe(true);

    // Further timer advances should NOT spawn another connection
    vi.advanceTimersByTime(60_000);
    expect(FakeWebSocket.instances).toHaveLength(3);
  });

  it("manual close() prevents further reconnection", () => {
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      baseDelayMs: 50,
      jitter: false,
    });
    sock.connect();
    FakeWebSocket.instances[0]!.fireOpen();

    sock.close(1000, "manual");
    expect(FakeWebSocket.instances[0]!.readyState).toBe(FakeWebSocket.CLOSED);

    vi.advanceTimersByTime(10_000);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("invokes onTokenRefreshNeeded on close code 4401 then reconnects", async () => {
    const onTokenRefreshNeeded = vi.fn().mockResolvedValue(undefined);
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      baseDelayMs: 50,
      maxRetries: 5,
      jitter: false,
      onTokenRefreshNeeded,
    });
    sock.connect();
    FakeWebSocket.instances[0]!.fireOpen();
    FakeWebSocket.instances[0]!.fireClose(TOKEN_REFRESH_CLOSE_CODE, "token-expired");

    expect(onTokenRefreshNeeded).toHaveBeenCalledTimes(1);

    // Allow the awaited microtask to settle
    await vi.runAllTimersAsync();

    // Token refresh should have produced a new socket without scheduling a normal backoff
    expect(FakeWebSocket.instances.length).toBeGreaterThanOrEqual(2);
  });

  it("supports url callback for candidate URL fallback", () => {
    const candidates = ["ws://primary/x", "ws://fallback/x"];
    let idx = 0;
    const sock = new ReconnectingSocket({
      url: () => candidates[idx]!,
      baseDelayMs: 30,
      jitter: false,
      onClose: () => {
        idx = (idx + 1) % candidates.length;
      },
    });
    sock.connect();
    expect(FakeWebSocket.instances[0]!.url).toBe("ws://primary/x");

    FakeWebSocket.instances[0]!.fireClose(1006);
    vi.advanceTimersByTime(30);
    expect(FakeWebSocket.instances[1]!.url).toBe("ws://fallback/x");
  });

  it("sends heartbeat ping on interval when configured", () => {
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      heartbeatIntervalMs: 1_000,
      heartbeatPing: () => JSON.stringify({ type: "ping" }),
    });
    sock.connect();
    const ws = FakeWebSocket.instances[0]!;
    ws.fireOpen();

    expect(ws.sent).toHaveLength(0);
    vi.advanceTimersByTime(1_000);
    expect(ws.sent).toHaveLength(1);
    expect(ws.sent[0]).toBe(JSON.stringify({ type: "ping" }));

    vi.advanceTimersByTime(1_000);
    expect(ws.sent).toHaveLength(2);
  });

  it("triggers reconnect when no pong received within heartbeatTimeoutMs", () => {
    const isPong = vi.fn((data: unknown) =>
      typeof data === "object" && data !== null && (data as { type?: string }).type === "pong"
    );
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      heartbeatIntervalMs: 500,
      heartbeatTimeoutMs: 1_500,
      heartbeatPing: () => JSON.stringify({ type: "ping" }),
      isPongMessage: isPong,
      baseDelayMs: 100,
      jitter: false,
    });
    sock.connect();
    const ws = FakeWebSocket.instances[0]!;
    ws.fireOpen();

    // No pong arrives → after timeout, socket gets closed with code 4000
    vi.advanceTimersByTime(1_500);
    expect(ws.readyState).toBe(FakeWebSocket.CLOSED);

    // After backoff a new socket is created
    vi.advanceTimersByTime(100);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("does not close on heartbeat timeout when pong is received", () => {
    const isPong = vi.fn((data: unknown) =>
      typeof data === "object" && data !== null && (data as { type?: string }).type === "pong"
    );
    const sock = new ReconnectingSocket({
      url: "ws://test/x",
      heartbeatIntervalMs: 500,
      heartbeatTimeoutMs: 1_500,
      heartbeatPing: () => JSON.stringify({ type: "ping" }),
      isPongMessage: isPong,
      jitter: false,
    });
    sock.connect();
    const ws = FakeWebSocket.instances[0]!;
    ws.fireOpen();

    // Send pong every 500ms to keep the connection alive
    for (let i = 0; i < 3; i += 1) {
      vi.advanceTimersByTime(500);
      ws.fireMessage(JSON.stringify({ type: "pong" }));
    }
    // 1.5s elapsed but pong refreshed timeout each time → still open
    expect(ws.readyState).toBe(FakeWebSocket.OPEN);
  });

  it("send() returns false when socket is not open", () => {
    const sock = new ReconnectingSocket({ url: "ws://test/x" });
    sock.connect();
    // CONNECTING state
    expect(sock.send("hi")).toBe(false);
    FakeWebSocket.instances[0]!.fireOpen();
    expect(sock.send("hi")).toBe(true);
    expect(FakeWebSocket.instances[0]!.sent).toEqual(["hi"]);
  });
});
