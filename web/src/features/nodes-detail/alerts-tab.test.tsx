import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AlertsTab from "./alerts-tab";

const { mockGetAlerts } = vi.hoisted(() => ({
  mockGetAlerts: vi.fn().mockResolvedValue([]),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: { getAlerts: mockGetAlerts },
}));

const makeAlert = (overrides: { id?: string; nodeId?: number; status?: string; message?: string }) => ({
  id: "alert-1",
  nodeId: 1,
  nodeName: "node-1",
  taskId: null,
  taskRunId: null,
  policyName: "probe",
  severity: "critical" as const,
  status: "open" as const,
  errorCode: "SSH_TIMEOUT",
  message: "连接超时",
  triggeredAt: "2024-01-01T10:00:00Z",
  retryable: false,
  ...overrides,
});

describe("AlertsTab", () => {
  beforeEach(() => {
    sessionStorage.setItem("xirang-auth-token", "test-token");
    mockGetAlerts.mockResolvedValue([]);
  });

  afterEach(() => {
    sessionStorage.removeItem("xirang-auth-token");
  });

  test("renders filter chips and empty state", async () => {
    render(
      <MemoryRouter>
        <AlertsTab nodeId={1} />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("alerts-filter-open")).toBeInTheDocument();
    expect(screen.getByTestId("alerts-filter-acked")).toBeInTheDocument();
    expect(screen.getByTestId("alerts-filter-resolved")).toBeInTheDocument();
    expect(await screen.findByText(/暂无未处理告警/)).toBeInTheDocument();
  });

  test("renders matching alerts and filters out other nodes", async () => {
    mockGetAlerts.mockResolvedValueOnce([
      makeAlert({ id: "alert-1", nodeId: 1, message: "连接超时" }),
      makeAlert({ id: "alert-2", nodeId: 99, message: "磁盘满" }),
    ]);

    render(
      <MemoryRouter>
        <AlertsTab nodeId={1} />
      </MemoryRouter>,
    );

    expect(await screen.findByText("连接超时")).toBeInTheDocument();
    expect(screen.queryByText("磁盘满")).not.toBeInTheDocument();
    expect(screen.getByTestId("alert-jump-alert-1")).toBeInTheDocument();
  });
});
