import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { AlertLogsPanel } from "./logs-page.alert";

const searchParamsRef = { current: new URLSearchParams() };

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useSearchParams: () => [searchParamsRef.current, vi.fn()] as const,
  };
});

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

const getAlertLogsMock = vi.fn();

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      getAlertLogs: (...args: unknown[]) => getAlertLogsMock(...args),
    },
  };
});

vi.mock("@/context/nodes-context", () => ({
  useNodesContext: () => ({
    nodes: [
      {
        id: 1,
        name: "web-01",
        host: "web-01.example.com",
        address: "10.0.0.1",
        ip: "10.0.0.1",
        port: 22,
        username: "root",
        authType: "key",
        tags: [],
        status: "online",
        lastSeenAt: "2026-04-20T00:00:00Z",
      },
    ],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    triggerNodeBackup: vi.fn(),
  }),
}));

beforeEach(() => {
  getAlertLogsMock.mockReset();
  searchParamsRef.current = new URLSearchParams();
});

describe("AlertLogsPanel", () => {
  it("renders 3 log rows and header when alert has node_id=1", async () => {
    searchParamsRef.current = new URLSearchParams("alert_id=42");

    getAlertLogsMock.mockResolvedValueOnce({
      data: [
        {
          id: 1,
          node_id: 1,
          source: "journalctl",
          path: "/var/log/syslog",
          timestamp: "2026-04-20T10:00:00Z",
          priority: "err",
          message: "disk full",
          created_at: "2026-04-20T10:00:00Z",
        },
        {
          id: 2,
          node_id: 1,
          source: "journalctl",
          path: "/var/log/syslog",
          timestamp: "2026-04-20T10:01:00Z",
          priority: "warning",
          message: "high load",
          created_at: "2026-04-20T10:01:00Z",
        },
        {
          id: 3,
          node_id: 1,
          source: "file",
          path: "/var/log/app.log",
          timestamp: "2026-04-20T10:02:00Z",
          priority: "info",
          message: "service restarted",
          created_at: "2026-04-20T10:02:00Z",
        },
      ],
      node_id: 1,
      window_start: "2026-04-20T09:55:00Z",
      window_end: "2026-04-20T10:05:00Z",
    });

    render(<AlertLogsPanel />);

    await waitFor(() => {
      expect(screen.getByText("disk full")).toBeInTheDocument();
      expect(screen.getByText("high load")).toBeInTheDocument();
      expect(screen.getByText("service restarted")).toBeInTheDocument();
    });

    expect(screen.getAllByText(/web-01/).length).toBeGreaterThan(0);
    expect(getAlertLogsMock).toHaveBeenCalledWith(
      "test-token",
      42,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
  });

  it("renders platform hint and no table rows when node_id=0", async () => {
    searchParamsRef.current = new URLSearchParams("alert_id=99");

    getAlertLogsMock.mockResolvedValueOnce({
      data: [],
      node_id: 0,
      window_start: "2026-04-20T09:55:00Z",
      window_end: "2026-04-20T10:05:00Z",
      hint: "平台告警无关联节点日志，请切换到「节点日志」tab 按时间查询",
    });

    render(<AlertLogsPanel />);

    await waitFor(() => {
      expect(
        screen.getByText(/平台告警无关联节点日志|Platform-level alert/i),
      ).toBeInTheDocument();
    });

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
