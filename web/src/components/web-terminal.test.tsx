import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import WebTerminal from "./web-terminal";

const { ensureStepUpProofMock, clearStepUpProofMock, requestTerminalCredentialGrantMock, socketInstances } = vi.hoisted(() => {
  const instances: Array<{
    options: {
      url: string;
      binaryType?: BinaryType;
      heartbeatIntervalMs?: number;
      onOpen?: (socket: { send: (value: string) => void }) => void;
      onClose?: (event: { code: number; reason: string }) => void;
    };
    sent: string[];
    closed: boolean;
    connect: () => void;
    send: (value: string) => boolean;
    close: () => void;
  }> = [];

  return {
    ensureStepUpProofMock: vi.fn(),
    clearStepUpProofMock: vi.fn(),
    requestTerminalCredentialGrantMock: vi.fn(),
    socketInstances: instances,
  };
});

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    ensureStepUpProof: ensureStepUpProofMock,
    clearStepUpProof: clearStepUpProofMock,
  }),
}));

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
    onData = vi.fn();
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

describe("WebTerminal", () => {
  beforeEach(() => {
    ensureStepUpProofMock.mockReset();
    clearStepUpProofMock.mockReset();
    requestTerminalCredentialGrantMock.mockReset();
    socketInstances.length = 0;
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

    await waitFor(() => expect(socketInstances).toHaveLength(1));
    expect(clearStepUpProofMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.terminalOpen);
    expect(socketInstances[0].options.url).toBe("wss://ops.example.test/api/v1/ws/terminal?node_id=7");
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
    const onDisconnect = vi.fn();
    render(<WebTerminal nodeId={7} token="token-1" onDisconnect={onDisconnect} />);

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
    expect(onDisconnect).not.toHaveBeenCalled();
    expect(await screen.findByRole("dialog", { name: "需要终端临时授权" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("授权原因"), "处理告警");
    await user.click(screen.getByRole("button", { name: "申请并重试" }));

    await waitFor(() => expect(requestTerminalCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(apiClient.requestTerminalCredentialGrant).toHaveBeenCalledWith(
      "token-1",
      { nodeId: 7, reason: "处理告警", requestedTtlSeconds: 600 },
      "proof-1",
    );
    await waitFor(() => expect(socketInstances).toHaveLength(2));
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(3);
    expect(ensureStepUpProofMock).toHaveBeenNthCalledWith(1, STEP_UP_ACTIONS.terminalOpen);
    expect(ensureStepUpProofMock).toHaveBeenNthCalledWith(2, STEP_UP_ACTIONS.terminalOpen);
    expect(ensureStepUpProofMock).toHaveBeenNthCalledWith(3, STEP_UP_ACTIONS.terminalOpen);
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
});
