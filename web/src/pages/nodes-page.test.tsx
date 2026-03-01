import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { NodesPage } from "./nodes-page";

const contextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext,
};
const searchParamsRef = { current: new URLSearchParams() };
const setSearchParamsMock = vi.fn();
const confirmMock = vi.fn().mockResolvedValue(true);
const navigateMock = vi.fn();

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useOutletContext: () => contextRef.current,
    useSearchParams: () => [searchParamsRef.current, setSearchParamsMock] as const,
    useNavigate: () => navigateMock,
  };
});

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/node-editor-dialog", () => ({
  NodeEditorDialog: () => null,
}));

vi.mock("@/pages/nodes-page.components", () => ({
  MobileNodeSearchDrawer: () => null,
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

function createContext(overrides?: Partial<ConsoleOutletContext>) {
  const base = {
    nodes: [
      {
        id: 1,
        name: "node-prod-1",
        host: "node-prod-1.example.com",
        address: "10.0.0.1",
        ip: "10.0.0.1",
        port: 22,
        username: "root",
        authType: "key",
        keyId: "key-1",
        tags: ["prod"],
        status: "online" as const,
        successRate: 99,
        lastSeenAt: "2026-02-24 12:00:00",
        lastBackupAt: "2026-02-24 11:50:00",
        diskFreePercent: 80,
        diskUsedGb: 100,
        diskTotalGb: 500,
        diskProbeAt: "2026-02-24 11:55:00",
        connectionLatencyMs: 12,
      },
      {
        id: 2,
        name: "node-dr-2",
        host: "node-dr-2.example.com",
        address: "10.0.0.2",
        ip: "10.0.0.2",
        port: 22,
        username: "backup",
        authType: "key",
        keyId: "key-1",
        tags: ["dr"],
        status: "warning" as const,
        successRate: 93,
        lastSeenAt: "2026-02-24 12:00:00",
        lastBackupAt: "2026-02-24 11:40:00",
        diskFreePercent: 42,
        diskUsedGb: 210,
        diskTotalGb: 500,
        diskProbeAt: "2026-02-24 11:56:00",
        connectionLatencyMs: 20,
      },
    ],
    sshKeys: [
      {
        id: "key-1",
        name: "主机密钥",
      },
    ],
    loading: false,
    globalSearch: "",
    createNode: vi.fn().mockResolvedValue(3),
    updateNode: vi.fn().mockResolvedValue(undefined),
    deleteNode: vi.fn().mockResolvedValue(undefined),
    deleteNodes: vi.fn().mockResolvedValue({
      deleted: 0,
      notFoundIds: [],
    }),
    testNodeConnection: vi.fn().mockResolvedValue({
      ok: true,
      message: "连接成功",
    }),
    triggerNodeBackup: vi.fn().mockResolvedValue(undefined),
  } as unknown as ConsoleOutletContext;

  contextRef.current = {
    ...base,
    ...overrides,
  } as ConsoleOutletContext;
}

describe("NodesPage", () => {
  beforeEach(() => {
    localStorage.clear();
    confirmMock.mockClear();
    navigateMock.mockReset();
    setSearchParamsMock.mockReset();
    searchParamsRef.current = new URLSearchParams();
    createContext();
  });

  it("视图切换具备语义角色并持久化选择", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <NodesPage />
      </MemoryRouter>
    );

    expect(
      screen.getByRole("radiogroup", { name: "节点视图切换" })
    ).toBeInTheDocument();

    const cardsButton = screen.getByRole("radio", { name: "节点卡片视图" });
    const listButton = screen.getByRole("radio", { name: "节点列表视图" });

    expect(cardsButton).toHaveAttribute("aria-checked", "true");
    expect(listButton).toHaveAttribute("aria-checked", "false");

    await user.click(listButton);

    expect(cardsButton).toHaveAttribute("aria-checked", "false");
    expect(listButton).toHaveAttribute("aria-checked", "true");
    expect(localStorage.getItem("xirang.nodes.view")).toBe(
      JSON.stringify("list")
    );
  });

  it("点击日志按钮会跳转到对应节点日志页", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <NodesPage />
      </MemoryRouter>
    );

    const logButtons = screen.getAllByRole("button", {
      name: "查看节点 node-prod-1 日志",
    });
    await user.click(logButtons[0]);

    expect(navigateMock).toHaveBeenCalledWith("/app/logs?node=node-prod-1");
  });
});
