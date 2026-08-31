import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { ReportsPage } from "./reports-page";

vi.mock("./reports-page.slo", () => ({
  SLOPanel: () => <div>SLO panel</div>,
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token", role: "admin" }),
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: vi.fn().mockResolvedValue(true),
    dialog: null,
  }),
}));

vi.mock("@/lib/api/reports-api", () => ({
  createReportsApi: () => ({
    listConfigs: vi.fn().mockResolvedValue([]),
    listReports: vi.fn().mockResolvedValue([]),
    deleteConfig: vi.fn().mockResolvedValue(undefined),
    generateNow: vi.fn().mockResolvedValue(undefined),
  }),
}));

vi.mock("@/components/report-config-dialog", () => ({
  ReportConfigDialog: () => null,
}));

describe("ReportsPage", () => {
  function renderReportsPage(initialEntry = "/app/reports") {
    const router = createMemoryRouter(
      [{ path: "/app/reports", element: <ReportsPage /> }],
      { initialEntries: [initialEntry] }
    );
    return {
      router,
      ...render(<RouterProvider router={router} />),
    };
  }

  it("renders workbench header and defaults to the SLA tab panel", async () => {
    renderReportsPage();

    expect(
      screen.getByRole("heading", { name: "报告工作台" })
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(
      screen.getByRole("tablist", { name: "报告视图" })
    ).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "SLA 报告" })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    expect(
      screen.getByRole("tabpanel", { name: "SLA 报告" })
    ).toBeInTheDocument();
    expect(await screen.findByText("暂无报告配置")).toBeInTheDocument();
    expect(screen.getByText("SLA 报告配置")).toBeInTheDocument();
  });

  it("switches to the SLO tab without losing tab semantics", async () => {
    const user = userEvent.setup();
    renderReportsPage();

    expect(await screen.findByText("暂无报告配置")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "SLO 目标" }));

    expect(screen.getByRole("tab", { name: "SLO 目标" })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    expect(
      screen.getByRole("tabpanel", { name: "SLO 目标" })
    ).toBeInTheDocument();
    expect(screen.getByText("SLO panel")).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("supports arrow-key navigation across report tabs", async () => {
    const user = userEvent.setup();
    renderReportsPage();

    expect(await screen.findByText("暂无报告配置")).toBeInTheDocument();

    screen.getByRole("tab", { name: "SLA 报告" }).focus();
    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("tab", { name: "SLO 目标" })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    expect(screen.getByText("SLO panel")).toBeInTheDocument();
  });

  it("preserves unrelated query params when switching report tabs", async () => {
    const user = userEvent.setup();
    const { router } = renderReportsPage("/app/reports?tab=sla&range=30d");

    expect(await screen.findByText("暂无报告配置")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "SLO 目标" }));

    expect(router.state.location.search).toBe("?tab=slo&range=30d");
  });
});
