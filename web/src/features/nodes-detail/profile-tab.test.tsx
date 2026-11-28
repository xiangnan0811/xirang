import { describe, test, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import type { NodeRecord } from "@/types/domain";
import ProfileTab from "./profile-tab";

const BASE_NODE: NodeRecord = {
  id: 1,
  name: "demo-node",
  host: "127.0.0.1",
  address: "127.0.0.1",
  ip: "127.0.0.1",
  port: 22,
  username: "root",
  authType: "key",
  keyId: null,
  basePath: "/",
  status: "online",
  tags: ["prod"],
  lastSeenAt: "2024-01-01T10:00:00Z",
  lastBackupAt: "2024-01-01T09:00:00Z",
  connectionLatencyMs: 12,
  archived: false,
  backupDir: "/backups",
  useSudo: false,
};

function valueOf(label: string): string | null | undefined {
  return screen.getByText(label, { selector: "dt" }).nextElementSibling?.textContent;
}

describe("ProfileTab", () => {
  test("renders node basics when found", () => {
    render(<ProfileTab node={BASE_NODE} isLoading={false} loadFailed={false} />);
    expect(screen.getByText("demo-node")).toBeInTheDocument();
    expect(screen.getByText("127.0.0.1:22")).toBeInTheDocument();
    expect(screen.getByText("prod")).toBeInTheDocument();
    expect(screen.getByText("/backups")).toBeInTheDocument();
    expect(screen.getByText("online")).toBeInTheDocument();
    expect(valueOf("归档")).toBe("否");
  });

  test("renders an archived node with missing recorded times as dashes", () => {
    render(
      <ProfileTab
        node={{ ...BASE_NODE, archived: true, lastSeenAt: "-", lastBackupAt: "-" }}
        isLoading={false}
        loadFailed={false}
      />
    );
    expect(valueOf("归档")).toBe("是");
    expect(valueOf("最近手动连接测试")).toBe("-");
    expect(valueOf("最近备份")).toBe("-");
    expect(screen.queryByText(/Invalid Date/)).not.toBeInTheDocument();
  });

  test("shows maintenance window when set", () => {
    render(
      <ProfileTab
        node={{ ...BASE_NODE, maintenanceStart: "02:00", maintenanceEnd: "04:00" }}
        isLoading={false}
        loadFailed={false}
      />
    );
    expect(valueOf("维护窗口")).toBe("02:00 → 04:00");
  });

  test("shows loading instead of a record while the page request is pending", () => {
    render(<ProfileTab node={null} isLoading loadFailed={false} />);
    expect(screen.getByText("加载中…")).toBeInTheDocument();
    expect(screen.queryByTestId("profile-tab")).not.toBeInTheDocument();
  });

  test("renders not-found when there is no record", () => {
    render(<ProfileTab node={null} isLoading={false} loadFailed={false} />);
    expect(screen.getByText("未找到该节点。")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  test("renders the load failure as an alert", () => {
    render(<ProfileTab node={null} isLoading={false} loadFailed />);
    expect(screen.getByRole("alert")).toHaveTextContent("属性加载失败");
  });
});
