import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { ReportsPage } from "./reports-page";
import i18n from "@/i18n";

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

const getIntegrations = vi.hoisted(() => vi.fn().mockResolvedValue([]));

vi.mock("@/lib/api/integrations-api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/integrations-api")>(
    "@/lib/api/integrations-api",
  );
  return {
    ...actual,
    createIntegrationsApi: () => ({
      ...actual.createIntegrationsApi(),
      getIntegrations,
    }),
  };
});

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

  it("focuses scope and period from their labels and keeps the selected values", async () => {
    const pendingIntegrations: { (channels: string[]): void }[] = [];
    getIntegrations.mockReset();
    getIntegrations.mockImplementation(
      () =>
        new Promise((resolve) => {
          pendingIntegrations.push(resolve);
        }),
    );

    const user = userEvent.setup();
    renderReportsPage();

    expect(await screen.findByText(i18n.t("reports.emptyTitle"))).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: i18n.t("reports.addConfig") }));
    expect(
      await screen.findByRole("dialog", { name: i18n.t("reportConfig.title") }),
    ).toBeInTheDocument();

    const scopeName = i18n.t("reportConfig.scope");
    const periodName = i18n.t("reportConfig.period");

    await user.click(screen.getByText(scopeName));
    const scopeSelect = screen.getByRole("combobox", { name: scopeName });
    expect(scopeSelect).toBeInstanceOf(HTMLSelectElement);
    expect(scopeSelect).toHaveFocus();
    expect(scopeSelect).toHaveValue("all");

    await user.selectOptions(scopeSelect, i18n.t("reports.scopeLabels.tag"));
    expect(scopeSelect).toHaveValue("tag");

    await user.click(screen.getByText(periodName));
    const periodSelect = screen.getByRole("combobox", { name: periodName });
    expect(periodSelect).toBeInstanceOf(HTMLSelectElement);
    expect(periodSelect).toHaveFocus();
    expect(periodSelect).toHaveValue("weekly");

    await user.selectOptions(periodSelect, i18n.t("reports.periodLabels.monthly"));
    expect(periodSelect).toHaveValue("monthly");
    expect(scopeSelect).toHaveValue("tag");

    await act(async () => {
      for (const resolve of pendingIntegrations) {
        resolve([]);
      }
    });

    expect(await screen.findByText(i18n.t("reportConfig.channelsEmpty"))).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: scopeName })).toHaveValue("tag");
    expect(screen.getByRole("combobox", { name: periodName })).toHaveValue("monthly");
  });
});
