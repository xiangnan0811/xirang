import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SLOPanel } from "./reports-page.slo";

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token", role: "admin" }),
}));

vi.mock("@/lib/api/slo", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/slo")>("@/lib/api/slo");
  return {
    ...actual,
    parseSLOTags: (s: { match_tags: unknown }) =>
      Array.isArray(s.match_tags)
        ? s.match_tags
        : s.match_tags
          ? JSON.parse(s.match_tags as string)
          : [],
  };
});

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listSLOs: vi.fn().mockResolvedValue([
        {
          id: 1,
          name: "prod availability",
          metric_type: "availability",
          match_tags: '["prod"]',
          threshold: 0.999,
          window_days: 28,
          enabled: true,
          created_by: 1,
          created_at: "2026-04-20T00:00:00Z",
          updated_at: "2026-04-20T00:00:00Z",
        },
      ]),
      getSLOSummary: vi.fn().mockResolvedValue({
        total: 1,
        healthy: 1,
        warning: 0,
        breached: 0,
        insufficient: 0,
      }),
      getSLOCompliance: vi.fn().mockResolvedValue({
        slo_id: 1,
        name: "prod availability",
        metric_type: "availability",
        window_start: "2026-03-23T00:00:00Z",
        window_end: "2026-04-20T00:00:00Z",
        threshold: 0.999,
        observed: 0.9995,
        sample_count: 33600,
        error_budget_remaining_pct: 50,
        burn_rate_1h: 0.1,
        status: "healthy",
      }),
      createSLO: vi.fn(),
      updateSLO: vi.fn(),
      deleteSLO: vi.fn(),
      listEscalationPolicies: vi.fn().mockResolvedValue([]),
    },
  };
});

describe("SLOPanel", () => {
  it("renders SLO list after load", async () => {
    render(<SLOPanel />);
    await waitFor(() => {
      expect(screen.getByText("prod availability")).toBeInTheDocument();
    });
  });

  it("opens create dialog when admin clicks new button", async () => {
    render(<SLOPanel />);
    await waitFor(() => expect(screen.getByText("prod availability")).toBeInTheDocument());
    // The "new" button text uses i18n key which renders as-is in tests without i18n provider
    // Find button by looking for one that is not edit/delete (which appear after data loads)
    const allButtons = screen.getAllByRole("button");
    // First button in CardHeader is the "new SLO" button
    expect(allButtons.length).toBeGreaterThan(0);
    // Click the first button which should be the "New SLO Target" button
    await userEvent.click(allButtons[0]);
    // Dialog should open - FormDialog renders a dialog role element
    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });
  });
});
