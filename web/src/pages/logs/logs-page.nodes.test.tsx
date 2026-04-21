import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NodeLogsPanel } from "./logs-page.nodes";

const setSearchParamsMock = vi.fn();
const searchParamsRef = { current: new URLSearchParams() };

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useSearchParams: () => [searchParamsRef.current, setSearchParamsMock] as const,
  };
});

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

const queryNodeLogsMock = vi.fn();

vi.mock("@/lib/api/node-logs", () => ({
  queryNodeLogs: (...args: unknown[]) => queryNodeLogsMock(...args),
}));

vi.mock("@/context/nodes-context", () => ({
  useNodesContext: () => ({
    nodes: [
      {
        id: 1,
        name: "node-1",
        host: "node-1.example.com",
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
  queryNodeLogsMock.mockReset();
  setSearchParamsMock.mockReset();
  searchParamsRef.current = new URLSearchParams();
});

describe("NodeLogsPanel", () => {
  it("renders filter UI and shows empty state when query returns 0 rows", async () => {
    queryNodeLogsMock.mockResolvedValueOnce({ data: [], total: 0, has_more: false });

    const user = userEvent.setup();
    render(<NodeLogsPanel />);

    expect(screen.getByRole("button", { name: /应用筛选|Apply/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /重置|Reset/i })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /应用筛选|Apply/i }));

    await waitFor(() => {
      expect(screen.getByText(/无匹配日志|No matching logs/i)).toBeInTheDocument();
    });
  });

  it("calls queryNodeLogs with expected query when Apply is clicked", async () => {
    queryNodeLogsMock.mockResolvedValueOnce({ data: [], total: 0, has_more: false });

    const user = userEvent.setup();
    render(<NodeLogsPanel />);

    await user.click(screen.getByRole("button", { name: /应用筛选|Apply/i }));

    await waitFor(() => {
      expect(queryNodeLogsMock).toHaveBeenCalledTimes(1);
      const [token, query] = queryNodeLogsMock.mock.calls[0] as [string, Record<string, unknown>];
      expect(token).toBe("test-token");
      expect(query.page).toBe(1);
      expect(query.page_size).toBe(50);
    });
  });

  it("increments page when next button is clicked", async () => {
    queryNodeLogsMock
      .mockResolvedValueOnce({
        data: Array.from({ length: 50 }, (_, i) => ({
          id: i + 1,
          node_id: 1,
          source: "journalctl",
          path: "/var/log/syslog",
          timestamp: "2026-04-20T10:00:00Z",
          priority: "info",
          message: `log line ${i + 1}`,
          created_at: "2026-04-20T10:00:00Z",
        })),
        total: 100,
        has_more: true,
      })
      .mockResolvedValueOnce({ data: [], total: 100, has_more: false });

    const user = userEvent.setup();
    render(<NodeLogsPanel />);

    await user.click(screen.getByRole("button", { name: /应用筛选|Apply/i }));

    await waitFor(() => {
      expect(queryNodeLogsMock).toHaveBeenCalledTimes(1);
    });

    const nextBtn = screen.getByRole("button", { name: "下一页" });
    await user.click(nextBtn);

    await waitFor(() => {
      expect(queryNodeLogsMock).toHaveBeenCalledTimes(2);
      const [, query] = queryNodeLogsMock.mock.calls[1] as [string, Record<string, unknown>];
      expect(query.page).toBe(2);
    });
  });
});
