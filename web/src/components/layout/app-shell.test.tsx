import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import * as React from "react";
import { AppShell } from "./app-shell";

const mockConsoleData = {
  globalSearch: "",
  setGlobalSearch: vi.fn(),
  refresh: vi.fn(),
  loading: false,
  nodes: [],
  overview: {
    healthyNodes: 0,
    runningTasks: 0,
    failedTasks24h: 0,
  },
  warning: null as string | null,
};

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    username: "alice",
    role: "admin",
    token: "token-1",
    totpEnabled: false,
    logout: vi.fn(),
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
  MobileNavigation: () => null,
}));

function renderShell() {
  return render(
    <MemoryRouter
      initialEntries={["/app/overview"]}
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <Routes>
        <Route path="/app" element={<AppShell />}>
          <Route path="overview" element={<div>概览内容</div>} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

describe("AppShell", () => {
  it("存在 warning 时为头部和侧边栏补足额外偏移", () => {
    mockConsoleData.warning = "节点同步接口超时";

    renderShell();

    const status = screen.getByRole("status");
    const header = status.closest("header");
    expect(header).toHaveClass("h-[88px]");

    const sidebar = screen
      .getByRole("button", { name: "收起侧边栏" })
      .closest("aside");
    expect(sidebar).toHaveClass("pt-[88px]");
  });
});
