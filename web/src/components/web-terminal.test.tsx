import { act, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import WebTerminal from "./web-terminal";

const { ensureStepUpProofMock, clearStepUpProofMock, socketInstances } = vi.hoisted(() => {
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
    socketInstances: instances,
  };
});

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    ensureStepUpProof: ensureStepUpProofMock,
    clearStepUpProof: clearStepUpProofMock,
  }),
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
    socketInstances.length = 0;
    ensureStepUpProofMock.mockResolvedValue("proof-1");
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
    expect(socketInstances[0].closed).toBe(true);
  });
});
