import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes, Link } from "react-router-dom";
import * as React from "react";
import { apiClient } from "@/lib/api/client";
import { AppShell } from "./app-shell";
import { appLayoutKey } from "./app-layout-key";

const mockConsoleData = {
  globalSearch: "",
  setGlobalSearch: vi.fn(),
  refresh: vi.fn(),
  loading: false,
  nodes: [],
  overview: {
    activePolicies: 0,
  },
  warning: null as string | null,
};

const { logoutMock } = vi.hoisted(() => ({
  logoutMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    username: "alice",
    role: "admin",
    token: "token-1",
    totpEnabled: false,
    authTransitioning: false,
    logout: logoutMock,
    setTotpEnabled: vi.fn(),
  }),
}));

vi.mock("@/hooks/use-console-data", () => ({
  useConsoleData: () => mockConsoleData,
}));

vi.mock("@/hooks/use-persistent-state", () => ({
  usePersistentState: <T,>(_: string, initialValue: T) => React.useState(initialValue),
}));

vi.mock("@/components/theme-toggle", () => ({
  ThemeToggle: () => <div data-testid="theme-toggle" />,
}));

vi.mock("@/components/display-preferences-toggle", () => ({
  DisplayPreferencesToggle: () => <div data-testid="display-toggle" />,
}));

vi.mock("@/components/scroll-to-top", () => ({
  ScrollToTop: () => null,
}));

vi.mock("@/components/error-boundary", () => ({
  ErrorBoundary: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

vi.mock("@/components/layout/mobile-navigation", () => ({
  MobileNavigation: ({ onLogout }: { onLogout: () => void }) => (
    <button type="button" onClick={() => void onLogout()}>测试退出</button>
  ),
}));

vi.mock("@/components/ui/command-palette", () => ({
  CommandPalette: () => null,
}));

function renderShell() {
  return render(
    <MemoryRouter
      initialEntries={["/app/overview"]}

>
      <Routes>
        <Route path="/login" element={<div>登录页</div>} />
        <Route path="/app" element={<AppShell />}>
          <Route path="overview" element={<div>概览内容</div>} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

describe("AppShell", () => {
  it("存在 warning 时保持头部和侧边栏高度稳定", () => {
    mockConsoleData.warning = "节点同步接口超时";

    renderShell();

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("节点同步接口超时");
    expect(status.closest("main")).toBeInTheDocument();
    expect(status.closest("header")).toBeNull();

    const header = document.querySelector("header");
    expect(header).toHaveClass("h-14");

    const sidebar = screen
      .getByRole("button", { name: "收起侧边栏" })
      .closest("aside");
    expect(sidebar).toHaveClass("pt-14");
  });

  it("keys nested /app sections so parent layouts survive tab changes", () => {
    expect(appLayoutKey("/app/backups/data")).toBe("/app/backups");
    expect(appLayoutKey("/app/backups/overview")).toBe("/app/backups");
    expect(appLayoutKey("/app/overview")).toBe("/app/overview");
  });

  it("does not remount a nested layout parent when switching child tabs", async () => {
    const user = userEvent.setup();
    let parentMounts = 0;
    function Parent() {
      React.useEffect(() => {
        parentMounts += 1;
      }, []);
      return (
        <div>
          <Link to="/app/backups/data">Data tab</Link>
          <Link to="/app/backups/overview">Overview tab</Link>
          <Outlet />
        </div>
      );
    }

    mockConsoleData.warning = null;
    render(
      <MemoryRouter initialEntries={["/app/backups/data"]}>
        <Routes>
          <Route path="/app" element={<AppShell />}>
            <Route path="backups" element={<Parent />}>
              <Route path="data" element={<div>data-panel</div>} />
              <Route path="overview" element={<div>overview-panel</div>} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("data-panel")).toBeInTheDocument();
    expect(parentMounts).toBe(1);
    await user.click(screen.getByRole("link", { name: "Overview tab" }));
    expect(await screen.findByText("overview-panel")).toBeInTheDocument();
    expect(parentMounts).toBe(1);
  });

  it("clears the local session before the backend logout request", async () => {
    const order: string[] = [];
    let resolve!: () => void;
    const promise = new Promise<void>((done) => {
      resolve = done;
    });
    logoutMock.mockImplementation(() => {
      order.push("local");
    });
    const logoutSpy = vi.spyOn(apiClient, "logout").mockImplementation((token: string) => {
      order.push(`api:${token}`);
      return promise;
    });
    mockConsoleData.warning = null;
    const user = userEvent.setup();
    try {
      renderShell();
      await user.click(screen.getByRole("button", { name: "测试退出" }));
      expect(order).toEqual(["local", "api:token-1"]);
      expect(screen.getByText("概览内容")).toBeInTheDocument();
      resolve();
      expect(await screen.findByText("登录页")).toBeInTheDocument();
    } finally {
      logoutSpy.mockRestore();
    }
  });
});
