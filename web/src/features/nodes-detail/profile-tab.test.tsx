import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import ProfileTab from "./profile-tab";

const BASE_NODE = {
  id: 1,
  name: "demo-node",
  host: "127.0.0.1",
  address: "127.0.0.1",
  ip: "127.0.0.1",
  port: 22,
  username: "root",
  authType: "key" as const,
  keyId: null,
  basePath: "/",
  status: "online" as const,
  tags: ["prod"],
  lastSeenAt: "2024-01-01T10:00:00Z",
  lastBackupAt: "2024-01-01T09:00:00Z",
  diskFreePercent: 40,
  diskUsedGb: 60,
  diskTotalGb: 100,
  lastProbeAt: "2024-01-01T10:00:00Z",
  archived: false,
  backupDir: "/backups",
  useSudo: false,
};

const { mockGetNodes } = vi.hoisted(() => ({
  mockGetNodes: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: { getNodes: mockGetNodes },
}));

describe("ProfileTab", () => {
  beforeEach(() => {
    sessionStorage.setItem("xirang-auth-token", "test-token");
    mockGetNodes.mockResolvedValue([BASE_NODE]);
  });

  afterEach(() => {
    sessionStorage.removeItem("xirang-auth-token");
  });

  test("renders node basics when found", async () => {
    render(<ProfileTab nodeId={1} />);
    expect(await screen.findByText("demo-node")).toBeInTheDocument();
    expect(screen.getByText(/127\.0\.0\.1/)).toBeInTheDocument();
    expect(screen.getByText("prod")).toBeInTheDocument();
    expect(screen.getByText("/backups")).toBeInTheDocument();
    expect(screen.getByText("online")).toBeInTheDocument();
  });

  test("renders not-found when node id missing", async () => {
    render(<ProfileTab nodeId={999} />);
    expect(await screen.findByText(/未找到该节点/)).toBeInTheDocument();
  });

  test("shows maintenance window when set", async () => {
    mockGetNodes.mockResolvedValueOnce([
      { ...BASE_NODE, id: 2, maintenanceStart: "02:00", maintenanceEnd: "04:00" },
    ]);
    render(<ProfileTab nodeId={2} />);
    expect(await screen.findByText(/02:00/)).toBeInTheDocument();
    expect(screen.getByText(/04:00/)).toBeInTheDocument();
  });
});
