import type { ReactElement } from "react";
import { StrictMode } from "react";
import { act, render as renderRoot, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { ApiError, bumpAuthSessionGeneration } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import WebTerminal from "./web-terminal";

const FRESH_PROOF = { persist: false, reuseCached: false };

const { ensureStepUpProofMock, clearStepUpProofMock, requestTerminalCredentialGrantMock, socketInstances, terminalInstances, authControls } = vi.hoisted(() => {
  const instances: Array<{
    options: {
      url: string;
      binaryType?: BinaryType;
      autoReconnect?: boolean;
      heartbeatIntervalMs?: number;
      onOpen?: (socket: { send: (value: string) => void }) => void;
      onClose?: (event: { code: number; reason: string }) => void;
      onGiveUp?: () => void;
      beforeConnect?: () => Promise<void> | void;
    };
    sent: string[];
    closed: boolean;
    connect: () => void;
    send: (value: string) => boolean;
    close: () => void;
  }> = [];
  const terminals: Array<{
    emitData: (data: string) => void;
    write: ReturnType<typeof vi.fn>;
    clear: ReturnType<typeof vi.fn>;
    dispose: ReturnType<typeof vi.fn>;
  }> = [];
  const authControls = {
    totpEnabled: true,
    authTransitioning: false,
    token: "token-1",
  };

  return {
    ensureStepUpProofMock: vi.fn(),
    clearStepUpProofMock: vi.fn(),
    requestTerminalCredentialGrantMock: vi.fn(),
    socketInstances: instances,
    terminalInstances: terminals,
    authControls,
  };
});

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    // 每次渲染换新函数身份，锁住“鉴权 helper 变化不得拆掉当前连接”。
    ensureStepUpProof: (...args: Parameters<typeof ensureStepUpProofMock>) => ensureStepUpProofMock(...args),
    clearStepUpProof: (...args: Parameters<typeof clearStepUpProofMock>) => clearStepUpProofMock(...args),
    totpEnabled: authControls.totpEnabled,
    authTransitioning: authControls.authTransitioning,
    token: authControls.token,
  }),
}));

function render(ui: ReactElement) {
  const view = renderRoot(<MemoryRouter initialEntries={["/app/nodes"]}>{ui}</MemoryRouter>);
  return {
    ...view,
    rerender(next: ReactElement) {
      view.rerender(<MemoryRouter initialEntries={["/app/nodes"]}>{next}</MemoryRouter>);
    },
  };
}

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    requestTerminalCredentialGrant: requestTerminalCredentialGrantMock,
  },
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class TerminalMock {
    cols = 80;
    rows = 24;
    loadAddon = vi.fn();
    open = vi.fn();
    write = vi.fn();
    clear = vi.fn();
    dispose = vi.fn();
    private onDataHandler: ((data: string) => void) | null = null;

    constructor() {
      terminalInstances.push(this);
    }

    onData(handler: (data: string) => void) {
      this.onDataHandler = handler;
    }

    emitData(data: string) {
      this.onDataHandler?.(data);
    }
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class FitAddonMock {
    fit = vi.fn();
  },
}));

vi.mock("@/lib/ws/reconnecting-socket", () => ({
  ReconnectingSocket: class ReconnectingSocketMock {
    readonly options: (typeof socketInstances)[number]["options"];
    sent: string[] = [];
    closed = false;

    constructor(options: (typeof socketInstances)[number]["options"]) {
      this.options = options;
      socketInstances.push(this);
    }

    connect() {
      const pending = this.options.beforeConnect?.();
      if (pending && typeof (pending as Promise<void>).then === "function") {
        void Promise.resolve(pending).then(
          () => {
            if (this.closed) {
              return;
            }
            this.options.onOpen?.({ send: (value: string) => this.sent.push(value) });
          },
          () => {
            if (this.closed) {
              return;
            }
            this.options.onGiveUp?.();
          },
        );
        return;
      }
      this.options.onOpen?.({ send: (value: string) => this.sent.push(value) });
    }

    send(value: string) {
      this.sent.push(value);
      return true;
    }

    close() {
      this.closed = true;
    }
  },
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function settleTerminal(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

async function flushTicks(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function stepUpRequiredError(): ApiError {
  return new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED" },
  });
}

describe("WebTerminal", () => {
  beforeEach(() => {
    ensureStepUpProofMock.mockReset();
    clearStepUpProofMock.mockReset();
    requestTerminalCredentialGrantMock.mockReset();
    socketInstances.length = 0;
    terminalInstances.length = 0;
    authControls.totpEnabled = true;
    authControls.authTransitioning = false;
    authControls.token = "token-1";
    sessionStorage.clear();
    localStorage.clear();
    ensureStepUpProofMock.mockResolvedValue("proof-1");
    requestTerminalCredentialGrantMock.mockResolvedValue({ id: 1, status: "active" });
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { protocol: "https:", host: "ops.example.test" },
    });
  });

  afterEach(() => {
    vi.clearAllTimers();
  });

  it("首条 WebSocket auth 消息会附加 step_up_proof", async () => {
    render(<WebTerminal nodeId={7} token="token-1" />);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(clearStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(clearStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen, FRESH_PROOF);
    expect(socketInstances[0].options.url).toBe("wss://ops.example.test/api/v1/ws/terminal?node_id=7");
    expect(socketInstances[0].options.autoReconnect).toBe(false);
    expect(socketInstances[0].options.heartbeatIntervalMs).toBe(0);
    expect(JSON.parse(socketInstances[0].sent[0] ?? "{}")).toEqual({
      type: "auth",
      token: "token-1",
      step_up_proof: "proof-1",
    });
  });

  it("policy violation close 会清理 proof", async () => {
    render(<WebTerminal nodeId={7} token="token-1" />);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(socketInstances).toHaveLength(1));
    clearStepUpProofMock.mockClear();
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "需要二次验证" });
    });

    expect(clearStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(clearStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen);
    expect(socketInstances[0].closed).toBe(true);
  });

  it("grant-required close 会打开授权原因弹窗、申请授权并重试终端连接", async () => {
    const user = userEvent.setup();
    render(<WebTerminal nodeId={7} token="token-1" />);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(socketInstances).toHaveLength(1));
    clearStepUpProofMock.mockClear();
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });

    expect(clearStepUpProofMock).not.toHaveBeenCalled();
    expect(socketInstances[0].closed).toBe(true);
    expect(await screen.findByRole("dialog", { name: "需要终端临时授权" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));

    await waitFor(() => expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(apiClient.requestTerminalCredentialGrant).toHaveBeenCalledWith(
      "token-1",
      { nodeId: 7, reason: "处理告警", requestedTtlSeconds: 600 },
      "proof-1",
    );
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(socketInstances).toHaveLength(2);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen, FRESH_PROOF);
    expect(JSON.parse(socketInstances[1]?.sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      token: "token-1",
      step_up_proof: "proof-1",
    });
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
    expect(JSON.stringify({ ...localStorage })).not.toContain("CREDENTIAL_GRANT_REQUIRED");
    expect(JSON.stringify({ ...sessionStorage })).not.toContain("CREDENTIAL_GRANT_REQUIRED");
    expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain("处理告警");
  });

  it("grant-required close 会清洗展示的 close reason 详情", async () => {
    render(<WebTerminal nodeId={7} token="token-1" />);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(socketInstances).toHaveLength(1));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:expired<script>" });
    });

    const dialog = await screen.findByRole("dialog", { name: "需要终端临时授权" });
    expect(dialog).toHaveTextContent("需要终端临时授权 (expiredscript)");
    expect(dialog).not.toHaveTextContent("<script>");
  });

  it("grant-required close 后提交空原因会显示校验错误且不申请授权", async () => {
    const user = userEvent.setup();
    render(<WebTerminal nodeId={7} token="token-1" />);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(socketInstances).toHaveLength(1));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:expired" });
    });

    await user.click(await screen.findByRole("button", { name: "申请并重试" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("请填写授权原因。");
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
  });

  it("F04 预置的 terminal proof 缓存不会被使用", async () => {
    const cachedProof = "cached-terminal-proof";
    sessionStorage.setItem(
      "xirang-step-up-proofs-v2",
      JSON.stringify({
        [STEP_UP_ACTIONS.terminalOpen]: { proof: cachedProof, expiresAt: Date.now() + 60_000 },
      }),
    );
    ensureStepUpProofMock.mockImplementation(async (_action: string, options?: { persist?: boolean; reuseCached?: boolean }) => {
      const persist = options?.persist ?? true;
      const reuseCached = options?.reuseCached ?? persist;
      if (reuseCached) {
        return cachedProof;
      }
      return "fresh-terminal-proof";
    });

    render(<WebTerminal nodeId={7} token="token-1" />);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));

    expect(JSON.parse(socketInstances[0].sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      token: "token-1",
      step_up_proof: "fresh-terminal-proof",
    });
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen, {
      persist: false,
      reuseCached: false,
    });
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).not.toContain("fresh-terminal-proof");
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("手动重连保留输出并取得新 proof，旧回调不能作用到新尝试", async () => {
    const gate = deferred<string>();
    ensureStepUpProofMock
      .mockResolvedValueOnce("proof-connect-1")
      .mockImplementationOnce(() => gate.promise);

    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(terminalInstances).toHaveLength(1);
    const oldSocket = socketInstances[0];

    expect(await screen.findByText("重新连接会建立新的 SSH 会话，不会恢复原来的终端。")).toBeInTheDocument();
    const reconnect = screen.getByRole("button", { name: "重新连接" });
    act(() => {
      reconnect.click();
      reconnect.click();
    });

    expect(socketInstances).toHaveLength(2);
    expect(oldSocket.closed).toBe(true);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(ensureStepUpProofMock).toHaveBeenNthCalledWith(2, STEP_UP_ACTIONS.terminalOpen, FRESH_PROOF);
    expect(terminalInstances).toHaveLength(1);
    expect(terminalInstances[0].dispose).not.toHaveBeenCalled();
    expect(terminalInstances[0].clear).not.toHaveBeenCalled();
    expect(terminalInstances[0].write).toHaveBeenCalledWith(expect.stringContaining("—— 新的 SSH 会话 ——"));

    act(() => {
      oldSocket.options.onOpen?.({ send: (value: string) => oldSocket.sent.push(value) });
      oldSocket.options.onClose?.({ code: 1011, reason: "stale-secret" });
    });
    expect(oldSocket.sent).toHaveLength(1);
    expect(screen.queryByText("stale-secret")).not.toBeInTheDocument();
    expect(screen.queryByText("终端连接失败 (1011)")).not.toBeInTheDocument();
    expect(socketInstances).toHaveLength(2);

    await act(async () => {
      gate.resolve("proof-connect-2");
    });
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(JSON.parse(socketInstances[1].sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      token: "token-1",
      step_up_proof: "proof-connect-2",
    });
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2") ?? "").not.toContain("proof-connect-2");
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("F04 OTP 未完成时键盘和 resize 不发帧，完成后首帧是 auth", async () => {
    const gate = deferred<string>();
    ensureStepUpProofMock.mockImplementation(() => gate.promise);
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();

    expect(socketInstances).toHaveLength(1);
    expect(socketInstances[0].sent).toEqual([]);
    expect(terminalInstances).toHaveLength(1);
    terminalInstances[0].emitData("ls\n");
    window.dispatchEvent(new Event("resize"));
    expect(socketInstances[0].sent).toEqual([]);
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();

    await act(async () => {
      gate.resolve("fresh-auth");
    });
    await waitFor(() => expect(socketInstances[0].sent.length).toBeGreaterThan(0));
    expect(JSON.parse(socketInstances[0].sent[0] ?? "{}")).toEqual({
      type: "auth",
      token: "token-1",
      step_up_proof: "fresh-auth",
    });
    expect(socketInstances[0].sent).toHaveLength(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen, FRESH_PROOF);
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2") ?? "").not.toContain("fresh-auth");
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();

    terminalInstances[0].emitData("ls\n");
    expect(socketInstances[0].sent[1]).toBe("ls\n");
  });

  it("延迟的 proof 在切换节点后不会连接旧操作", async () => {
    const gates: Array<ReturnType<typeof deferred<string>>> = [];
    ensureStepUpProofMock.mockImplementation(() => {
      const gate = deferred<string>();
      gates.push(gate);
      return gate.promise;
    });
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    expect(gates).toHaveLength(1);

    view.rerender(<WebTerminal nodeId={8} token="token-1" />);
    await settleTerminal();
    expect(gates).toHaveLength(2);

    await act(async () => {
      gates[0]?.resolve("stale-proof");
    });
    await flushTicks();
    expect(socketInstances[0]?.sent ?? []).toEqual([]);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);

    await act(async () => {
      gates[1]?.resolve("node-8-proof");
    });
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(socketInstances).toHaveLength(2);
    expect(socketInstances[1].options.url).toContain("node_id=8");
    expect(JSON.parse(socketInstances[1].sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      step_up_proof: "node-8-proof",
    });
    expect(socketInstances.flatMap((socket) => socket.sent).join("")).not.toContain("stale-proof");
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
  });

  it("延迟的 proof 在切换 token 后不会连接旧操作", async () => {
    const gates: Array<ReturnType<typeof deferred<string>>> = [];
    ensureStepUpProofMock.mockImplementation(() => {
      const gate = deferred<string>();
      gates.push(gate);
      return gate.promise;
    });
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    view.rerender(<WebTerminal nodeId={7} token="token-2" />);
    await settleTerminal();
    expect(gates).toHaveLength(2);

    await act(async () => {
      gates[0]?.resolve("stale-proof");
    });
    await flushTicks();
    expect(socketInstances[0]?.sent ?? []).toEqual([]);

    await act(async () => {
      gates[1]?.resolve("token-2-proof");
    });
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(JSON.parse(socketInstances[1].sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      token: "token-2",
      step_up_proof: "token-2-proof",
    });
    expect(socketInstances.flatMap((socket) => socket.sent).join("")).not.toContain("stale-proof");
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
  });

  it("延迟的 proof 在 session generation 变化后不会连接或再次请求", async () => {
    const gate = deferred<string>();
    ensureStepUpProofMock.mockImplementation(() => gate.promise);
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);

    bumpAuthSessionGeneration();
    await act(async () => {
      gate.resolve("stale-proof");
    });
    await flushTicks();

    expect(socketInstances[0]?.sent ?? []).toEqual([]);
    expect(socketInstances).toHaveLength(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(terminalInstances).toHaveLength(1);
    expect(terminalInstances[0]?.dispose).not.toHaveBeenCalled();
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByText("stale-proof")).not.toBeInTheDocument();
  });

  it("延迟的 proof 在卸载后不会连接或更新", async () => {
    const gate = deferred<string>();
    ensureStepUpProofMock.mockImplementation(() => gate.promise);
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    view.unmount();

    await act(async () => {
      gate.resolve("stale-proof");
    });
    await flushTicks();

    expect(socketInstances).toHaveLength(1);
    expect(socketInstances[0].sent).toEqual([]);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
  });

  it("延迟的授权申请在切换节点后不会重试连接或更新新操作", async () => {
    const user = userEvent.setup();
    const gate = deferred<{ id: number; status: string }>();
    ensureStepUpProofMock.mockResolvedValue("proof-1");
    requestTerminalCredentialGrantMock.mockImplementation(() => gate.promise);
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));
    await waitFor(() => expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1));

    view.rerender(<WebTerminal nodeId={8} token="token-1" />);
    await settleTerminal();
    await act(async () => {
      gate.resolve({ id: 1, status: "active" });
    });
    await flushTicks();

    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(socketInstances).toHaveLength(2);
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(socketInstances[1].options.url).toContain("node_id=8");
    expect(screen.queryByText("stale-grant-result")).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("延迟的授权申请在切换 token 后不会重试连接或更新新操作", async () => {
    const user = userEvent.setup();
    const gate = deferred<{ id: number; status: string }>();
    ensureStepUpProofMock.mockResolvedValue("proof-1");
    requestTerminalCredentialGrantMock.mockImplementation(() => gate.promise);
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));
    await waitFor(() => expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1));

    view.rerender(<WebTerminal nodeId={7} token="token-2" />);
    await settleTerminal();
    await act(async () => {
      gate.resolve({ id: 1, status: "active" });
    });
    await flushTicks();

    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1);
    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledWith(
      "token-1",
      { nodeId: 7, reason: "处理告警", requestedTtlSeconds: 600 },
      "proof-1",
    );
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(JSON.parse(socketInstances[1].sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      token: "token-2",
    });
    expect(socketInstances[0].sent).toHaveLength(1);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("延迟的授权申请在 session generation 变化后不会重试连接或更新", async () => {
    const user = userEvent.setup();
    const gate = deferred<{ id: number; status: string }>();
    ensureStepUpProofMock.mockResolvedValue("proof-1");
    requestTerminalCredentialGrantMock.mockImplementation(() => gate.promise);
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));
    await waitFor(() => expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1));

    bumpAuthSessionGeneration();
    await act(async () => {
      gate.reject(new Error("stale-grant-result"));
    });
    await flushTicks();

    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(socketInstances).toHaveLength(1);
    expect(socketInstances[0].sent).toHaveLength(1);
    expect(screen.queryByText("stale-grant-result")).not.toBeInTheDocument();
    expect(screen.getByLabelText("授权原因")).toHaveValue("处理告警");
  });

  it("延迟的授权申请在卸载后不会重试连接或更新", async () => {
    const user = userEvent.setup();
    const gate = deferred<{ id: number; status: string }>();
    ensureStepUpProofMock.mockResolvedValue("proof-1");
    requestTerminalCredentialGrantMock.mockImplementation(() => gate.promise);
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));
    await waitFor(() => expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1));

    view.unmount();
    await act(async () => {
      gate.resolve({ id: 1, status: "active" });
    });
    await flushTicks();

    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(socketInstances).toHaveLength(1);
  });

  it("授权失败会保留弹窗和原因，并在下次提交时重新获取 proof", async () => {
    const user = userEvent.setup();
    ensureStepUpProofMock
      .mockResolvedValueOnce("proof-1")
      .mockResolvedValueOnce("proof-2");
    requestTerminalCredentialGrantMock
      .mockRejectedValueOnce(new Error("授权服务不可用"))
      .mockResolvedValueOnce({ id: 1, status: "active" });

    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("授权服务不可用");
    expect(screen.getByRole("dialog", { name: "需要终端临时授权" })).toBeInTheDocument();
    expect(screen.getByLabelText("授权原因")).toHaveValue("处理告警");
    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1);
    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledWith(
      "token-1",
      { nodeId: 7, reason: "处理告警", requestedTtlSeconds: 600 },
      "proof-1",
    );
    expect(socketInstances).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "申请并重试" }));
    await waitFor(() => expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(2));
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(requestTerminalCredentialGrantMock).toHaveBeenLastCalledWith(
      "token-1",
      { nodeId: 7, reason: "处理告警", requestedTtlSeconds: 600 },
      "proof-2",
    );
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2") ?? "").not.toContain("proof-2");
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("取消授权会清除草稿并且不会重连", async () => {
    const user = userEvent.setup();
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "草稿原因");
    await user.click(screen.getByRole("button", { name: "取消" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByText("草稿原因")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText("授权已取消。重新连接会建立新的 SSH 会话。")).toBeInTheDocument();
    expect(socketInstances).toHaveLength(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2") ?? "").not.toContain("草稿原因");
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("proof 过期时只重新验证一次并继续本次授权", async () => {
    const user = userEvent.setup();
    ensureStepUpProofMock
      .mockResolvedValueOnce("proof-1")
      .mockResolvedValueOnce("proof-2");
    requestTerminalCredentialGrantMock
      .mockRejectedValueOnce(stepUpRequiredError())
      .mockResolvedValueOnce({ id: 1, status: "active" });

    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));

    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(ensureStepUpProofMock).toHaveBeenNthCalledWith(2, STEP_UP_ACTIONS.terminalOpen, FRESH_PROOF);
    expect(requestTerminalCredentialGrantMock).toHaveBeenNthCalledWith(
      1,
      "token-1",
      { nodeId: 7, reason: "处理告警", requestedTtlSeconds: 600 },
      "proof-1",
    );
    expect(requestTerminalCredentialGrantMock).toHaveBeenNthCalledWith(
      2,
      "token-1",
      { nodeId: 7, reason: "处理告警", requestedTtlSeconds: 600 },
      "proof-2",
    );
    expect(JSON.parse(socketInstances[1].sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      step_up_proof: "proof-2",
    });
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
  });

  it("续接再次要求授权时不会自动循环，需用户重新提交", async () => {
    const user = userEvent.setup();
    ensureStepUpProofMock
      .mockResolvedValueOnce("proof-1")
      .mockResolvedValueOnce("proof-3");
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    await user.type(await screen.findByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);

    act(() => {
      socketInstances[1].options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:again" });
    });
    await flushTicks();

    expect(await screen.findByRole("alert")).toHaveTextContent("需要终端临时授权 (again)");
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1);
    expect(socketInstances).toHaveLength(2);

    await user.type(screen.getByLabelText("授权原因"), "再次处理");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));
    await waitFor(() => expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2));
    expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(2);
    expect(requestTerminalCredentialGrantMock).toHaveBeenLastCalledWith(
      "token-1",
      { nodeId: 7, reason: "再次处理", requestedTtlSeconds: 600 },
      "proof-3",
    );
  });

  it("StrictMode 不会重复建立终端连接", async () => {
    render(
      <StrictMode>
        <WebTerminal nodeId={7} token="token-1" />
      </StrictMode>,
    );
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(socketInstances).toHaveLength(1);
    expect(terminalInstances).toHaveLength(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen, FRESH_PROOF);
  });

  it("取消二次验证会显示取消并且不会再次挑战", async () => {
    ensureStepUpProofMock.mockRejectedValueOnce(new Error("已取消二次验证。"));
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    expect(await screen.findByText("已取消二次验证。")).toBeInTheDocument();
    await flushTicks();
    expect(socketInstances[0]?.sent ?? []).toEqual([]);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("其他二次验证失败只显示安全摘要", async () => {
    ensureStepUpProofMock.mockRejectedValueOnce(new Error("服务器爆炸"));
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    expect(await screen.findByRole("alert")).toHaveTextContent("二次验证失败。");
    expect(screen.queryByText("服务器爆炸")).not.toBeInTheDocument();
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(socketInstances[0]?.sent ?? []).toEqual([]);
  });

  it("父级重渲染更换回调但节点令牌和会话不变时保持当前连接和授权弹窗", async () => {
    function TerminalParent({ nodeId, token }: { nodeId: number; token: string }) {
      return <WebTerminal nodeId={nodeId} token={token} />;
    }

    const view = render(<TerminalParent nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    const socket = socketInstances[0];
    act(() => {
      socket.options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    expect(await screen.findByRole("dialog", { name: "需要终端临时授权" })).toBeInTheDocument();
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(socket.closed).toBe(true);

    const closeAfterGrant = vi.spyOn(socket, "close");
    view.rerender(<TerminalParent nodeId={7} token="token-1" />);
    await settleTerminal();
    await flushTicks();

    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(clearStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(socketInstances).toHaveLength(1);
    expect(socketInstances[0]).toBe(socket);
    expect(socket.closed).toBe(true);
    expect(closeAfterGrant).not.toHaveBeenCalled();
    expect(terminalInstances).toHaveLength(1);
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "需要终端临时授权" })).toBeInTheDocument();
    expect(socket.sent).toHaveLength(1);
  });

  it("关闭后保留可复制的安全摘要，键盘和 resize 不再发帧", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const cases = [
      [1000, "连接已结束 (1000)", "exit", false],
      [1001, "连接已结束 (1001)", "going-away", false],
      [1006, "网络连接中断 (1006)", "RAW_NETWORK_SECRET", true],
      [1007, "节点不可用 (1007)", "RAW_NODE_SECRET", true],
      [1011, "终端连接失败 (1011)", "RAW_TERMINAL_SECRET", true],
      [4401, "终端连接已结束 (4401)", "token-expired-secret", false],
      [1012, "终端连接已结束 (1012)", "RAW_OTHER_SECRET", false],
    ] as const;

    for (const [code, message, raw, isAlert] of cases) {
      socketInstances.length = 0;
      terminalInstances.length = 0;
      ensureStepUpProofMock.mockClear();
      const view = render(<WebTerminal nodeId={7} token="token-1" />);
      await settleTerminal();
      await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
      const socket = socketInstances[0];
      act(() => {
        socket.options.onClose?.({ code, reason: raw });
      });

      const summary = await screen.findByText(message);
      expect(summary).toBeInTheDocument();
      expect(screen.queryByText(raw)).not.toBeInTheDocument();
      expect(isAlert ? summary.closest("[role='alert']") : summary.closest("[role='status']")).not.toBeNull();
      expect(socketInstances).toHaveLength(1);
      expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
      terminalInstances[0].emitData("ls\n");
      window.dispatchEvent(new Event("resize"));
      expect(socket.sent).toHaveLength(1);
      expect(socket.sent.join("")).not.toContain("resize");

      writeText.mockClear();
      await user.click(screen.getByRole("button", { name: "复制状态" }));
      expect(writeText).toHaveBeenCalledWith(message);
      view.unmount();
    }
  });

  it("1008 非授权关闭显示需重新验证且不展示原始原因", async () => {
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances).toHaveLength(1));
    clearStepUpProofMock.mockClear();
    act(() => {
      socketInstances[0].options.onClose?.({ code: 1008, reason: "需要二次验证" });
    });
    expect(await screen.findByText("需重新验证 (1008)")).toBeInTheDocument();
    expect(screen.queryByText("需要二次验证")).not.toBeInTheDocument();
    expect(clearStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen);
    expect(socketInstances).toHaveLength(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
  });

  it("未启用两步验证时不连接也不申请 proof", async () => {
    authControls.totpEnabled = false;
    render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await flushTicks();
    expect(socketInstances).toHaveLength(0);
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
    const notice = screen.getByRole("alert");
    expect(notice).toHaveClass("shrink-0");
    expect(notice).toHaveTextContent("需先启用两步验证。实际执行仍需二次验证及适用的授权原因。");
    expect(screen.getByRole("link", { name: "启用两步验证" })).toHaveAttribute("href", "/app/settings?tab=account");
  });

  it("登录会话更新时关闭旧 socket 且结束后不自动重连", async () => {
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    authControls.authTransitioning = true;
    view.rerender(<WebTerminal nodeId={7} token="token-1" />);
    expect(await screen.findByText("账户安全状态正在更新。请等待完成，此操作不会自动继续。")).toBeInTheDocument();
    expect(socketInstances[0].closed).toBe(true);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    terminalInstances[0].emitData("ls\n");
    window.dispatchEvent(new Event("resize"));
    expect(socketInstances[0].sent).toHaveLength(1);
    expect(terminalInstances[0].dispose).not.toHaveBeenCalled();

    authControls.authTransitioning = false;
    view.rerender(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await flushTicks();
    expect(socketInstances).toHaveLength(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
  });

  it("切换节点会清空终端输出", async () => {
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    view.rerender(<WebTerminal nodeId={8} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(terminalInstances[0].dispose).toHaveBeenCalledTimes(1);
    expect(terminalInstances).toHaveLength(2);
    expect(socketInstances[0].closed).toBe(true);
  });

  it("会话代际变化后放弃当前连接且不自动再次验证，手动重连才取得新 proof", async () => {
    const user = userEvent.setup();
    const view = render(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await waitFor(() => expect(socketInstances[0]?.sent.length ?? 0).toBeGreaterThan(0));
    const socket = socketInstances[0];
    act(() => {
      socket.options.onClose?.({ code: 1008, reason: "CREDENTIAL_GRANT_REQUIRED:required" });
    });
    expect(await screen.findByRole("dialog", { name: "需要终端临时授权" })).toBeInTheDocument();

    bumpAuthSessionGeneration();
    view.rerender(<WebTerminal nodeId={7} token="token-1" />);
    await settleTerminal();
    await flushTicks();

    expect(socket.closed).toBe(true);
    expect(socketInstances).toHaveLength(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(clearStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(terminalInstances).toHaveLength(1);
    expect(terminalInstances[0]?.dispose).not.toHaveBeenCalled();
    expect(requestTerminalCredentialGrantMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "需要终端临时授权" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重新连接" }));
    await waitFor(() => expect(socketInstances[1]?.sent.length ?? 0).toBeGreaterThan(0));
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(ensureStepUpProofMock).toHaveBeenLastCalledWith(STEP_UP_ACTIONS.terminalOpen, FRESH_PROOF);
    expect(clearStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(socketInstances[1].options.url).toContain("node_id=7");
    expect(JSON.parse(socketInstances[1].sent[0] ?? "{}")).toMatchObject({
      type: "auth",
      token: "token-1",
      step_up_proof: "proof-1",
    });
  });
});
