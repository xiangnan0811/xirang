import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { ReportsPage } from "./reports-page";
import type { Report, ReportConfig } from "@/lib/api/reports-api";
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

const listConfigs = vi.hoisted(() => vi.fn().mockResolvedValue([]));
const listReports = vi.hoisted(() => vi.fn().mockResolvedValue([]));
const deleteConfig = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
const generateNow = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));

vi.mock("@/lib/api/reports-api", () => ({
  createReportsApi: () => ({
    listConfigs,
    listReports,
    deleteConfig,
    generateNow,
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
  beforeEach(() => {
    listConfigs.mockReset();
    listConfigs.mockResolvedValue([]);
    listReports.mockReset();
    listReports.mockResolvedValue([]);
    deleteConfig.mockReset();
    deleteConfig.mockResolvedValue(undefined);
    generateNow.mockReset();
    generateNow.mockResolvedValue(undefined);
  });

  afterEach(async () => {
    await i18n.changeLanguage("zh");
  });

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

  describe("recovery objectives in expanded report row", () => {
    const mockConfig: ReportConfig = {
      id: 1,
      name: "Weekly SLA Report",
      scopeType: "all",
      scopeValue: "",
      period: "weekly",
      cron: "0 8 * * 1",
      integrationIds: [],
      enabled: true,
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-01T00:00:00Z",
    };

    const createMockReport = (overrides: Partial<Report> = {}): Report => ({
      id: 101,
      configId: 1,
      periodStart: "2026-03-01T00:00:00Z",
      periodEnd: "2026-03-07T23:59:59Z",
      totalRuns: 20,
      successRuns: 19,
      failedRuns: 1,
      successRate: 95,
      avgDurationMs: 1200,
      topFailures: [],
      generatedAt: "2026-03-08T00:00:00Z",
      createdAt: "2026-03-08T00:00:00Z",
      ...overrides,
    });

    it("renders 0/true (Compliant) and 120/false (Non-compliant) with semantic status badges", async () => {
      const user = userEvent.setup();
      listConfigs.mockResolvedValueOnce([mockConfig]);
      listReports.mockResolvedValueOnce([
        createMockReport({
          actualRpoMinutes: 0,
          rpoCompliant: true,
          actualRtoMinutes: 120,
          rtoCompliant: false,
        }),
      ]);

      renderReportsPage();

      expect(await screen.findByText(mockConfig.name)).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: i18n.t("reports.historyReports") }));
      const rowButton = await screen.findByRole("button", { name: /2026-03/ });
      await user.click(rowButton);

      const rpoGroup = screen.getByRole("group", {
        name: i18n.t("reports.recoveryObjectives.actualRpo"),
      });
      const rtoGroup = screen.getByRole("group", {
        name: i18n.t("reports.recoveryObjectives.actualRto"),
      });

      // 0 / true -> displays 0, Compliant
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.actualRpo"))).toBeInTheDocument();
      expect(within(rpoGroup).getByText("0")).toBeInTheDocument();
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.compliant"))).toBeInTheDocument();
      expect(within(rpoGroup).queryByText(i18n.t("reports.recoveryObjectives.insufficientEvidence"))).not.toBeInTheDocument();
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.rpoExplanation"))).toBeInTheDocument();

      // 120 / false -> displays 120, Non-compliant (no insufficient evidence because actual is present)
      expect(within(rtoGroup).getByText(i18n.t("reports.recoveryObjectives.actualRto"))).toBeInTheDocument();
      expect(within(rtoGroup).getByText("120")).toBeInTheDocument();
      expect(within(rtoGroup).getByText(i18n.t("reports.recoveryObjectives.nonCompliant"))).toBeInTheDocument();
      expect(within(rtoGroup).queryByText(i18n.t("reports.recoveryObjectives.insufficientEvidence"))).not.toBeInTheDocument();
      expect(within(rtoGroup).getByText(i18n.t("reports.recoveryObjectives.rtoExplanation"))).toBeInTheDocument();
    });

    it("renders null/false as Unknown, Non-compliant with insufficient evidence, and null/null as Unknown, Not assessed", async () => {
      const user = userEvent.setup();
      listConfigs.mockResolvedValueOnce([mockConfig]);
      listReports.mockResolvedValueOnce([
        createMockReport({
          actualRpoMinutes: null,
          rpoCompliant: false,
          actualRtoMinutes: null,
          rtoCompliant: null,
        }),
      ]);

      renderReportsPage();

      expect(await screen.findByText(mockConfig.name)).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: i18n.t("reports.historyReports") }));
      const rowButton = await screen.findByRole("button", { name: /2026-03/ });
      await user.click(rowButton);

      const rpoGroup = screen.getByRole("group", {
        name: i18n.t("reports.recoveryObjectives.actualRpo"),
      });
      const rtoGroup = screen.getByRole("group", {
        name: i18n.t("reports.recoveryObjectives.actualRto"),
      });

      // null / false -> displays Unknown, Non-compliant, and Insufficient evidence
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.unknown"))).toBeInTheDocument();
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.nonCompliant"))).toBeInTheDocument();
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.insufficientEvidence"))).toBeInTheDocument();

      // null / null -> displays Unknown, Not assessed, no insufficient evidence
      expect(within(rtoGroup).getByText(i18n.t("reports.recoveryObjectives.unknown"))).toBeInTheDocument();
      expect(within(rtoGroup).getByText(i18n.t("reports.recoveryObjectives.notAssessed"))).toBeInTheDocument();
      expect(within(rtoGroup).queryByText(i18n.t("reports.recoveryObjectives.insufficientEvidence"))).not.toBeInTheDocument();
    });

    it("treats missing actual with true compliance as Unknown and Not assessed without success badge, and supports undefined", async () => {
      const user = userEvent.setup();
      listConfigs.mockResolvedValueOnce([mockConfig]);
      listReports.mockResolvedValueOnce([
        createMockReport({
          actualRpoMinutes: null,
          rpoCompliant: true,
          actualRtoMinutes: undefined,
          rtoCompliant: undefined,
        }),
      ]);

      renderReportsPage();

      expect(await screen.findByText(mockConfig.name)).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: i18n.t("reports.historyReports") }));
      const rowButton = await screen.findByRole("button", { name: /2026-03/ });
      await user.click(rowButton);

      const rpoGroup = screen.getByRole("group", {
        name: i18n.t("reports.recoveryObjectives.actualRpo"),
      });
      const rtoGroup = screen.getByRole("group", {
        name: i18n.t("reports.recoveryObjectives.actualRto"),
      });

      // null / true: MUST display Unknown and Not assessed; MUST NOT display success badge
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.unknown"))).toBeInTheDocument();
      expect(within(rpoGroup).getByText(i18n.t("reports.recoveryObjectives.notAssessed"))).toBeInTheDocument();
      expect(within(rpoGroup).queryByText(i18n.t("reports.recoveryObjectives.compliant"))).not.toBeInTheDocument();
      expect(within(rpoGroup).queryByText(i18n.t("reports.recoveryObjectives.insufficientEvidence"))).not.toBeInTheDocument();

      // undefined / undefined: displays Unknown and Not assessed
      expect(within(rtoGroup).getByText(i18n.t("reports.recoveryObjectives.unknown"))).toBeInTheDocument();
      expect(within(rtoGroup).getByText(i18n.t("reports.recoveryObjectives.notAssessed"))).toBeInTheDocument();
      expect(within(rtoGroup).queryByText(i18n.t("reports.recoveryObjectives.compliant"))).not.toBeInTheDocument();
      expect(within(rtoGroup).queryByText(i18n.t("reports.recoveryObjectives.insufficientEvidence"))).not.toBeInTheDocument();
    });
  });
});
