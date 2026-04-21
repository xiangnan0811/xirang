import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { AlertEscalationTimeline } from "./alert-detail";

vi.mock("@/lib/api/escalation", () => ({
  listAlertEscalationEvents: vi.fn(),
  listEscalationPolicies: vi.fn().mockResolvedValue([]),
}));

import { listAlertEscalationEvents } from "@/lib/api/escalation";

const mockListEvents = listAlertEscalationEvents as ReturnType<typeof vi.fn>;

const baseEvent = {
  id: 1,
  alert_id: 42,
  escalation_policy_id: 1,
  integration_ids: [10],
  severity_before: "warning" as const,
  severity_after: "warning" as const,
  tags_added: [],
  fired_at: "2026-04-21T10:00:00Z",
};

describe("AlertEscalationTimeline", () => {
  it("renders 2 list items when 2 events are returned", async () => {
    mockListEvents.mockResolvedValue([
      { ...baseEvent, id: 1, level_index: 0 },
      { ...baseEvent, id: 2, level_index: 1 },
    ]);

    render(<AlertEscalationTimeline token="test-token" alertId={42} />);

    await waitFor(() => {
      // level_index 0 → "第 1 级", level_index 1 → "第 2 级"
      expect(screen.getByText(/第 1 级/)).toBeInTheDocument();
      expect(screen.getByText(/第 2 级/)).toBeInTheDocument();
    });
  });

  it("renders silenced-skip badge when integration_ids is empty", async () => {
    mockListEvents.mockResolvedValue([
      { ...baseEvent, id: 3, level_index: 0, integration_ids: [] },
    ]);

    render(<AlertEscalationTimeline token="test-token" alertId={42} />);

    await waitFor(() => {
      expect(screen.getByText(/静默跳过/)).toBeInTheDocument();
    });
  });

  it("shows empty state when no events are returned", async () => {
    mockListEvents.mockResolvedValue([]);

    render(<AlertEscalationTimeline token="test-token" alertId={42} />);

    await waitFor(() => {
      expect(screen.getByText(/暂无升级记录/)).toBeInTheDocument();
    });
  });
});
