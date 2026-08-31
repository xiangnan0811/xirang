import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { AlertEscalationTimeline } from "./alert-detail";

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listAlertEscalationEvents: vi.fn(),
      listEscalationPolicies: vi.fn().mockResolvedValue([]),
    },
  };
});

import { apiClient } from "@/lib/api/client";

const mockListEvents = apiClient.listAlertEscalationEvents as ReturnType<typeof vi.fn>;

const baseEvent = {
  id: 1,
  alertId: 42,
  escalationPolicyId: 1,
  integrationIds: [10],
  severityBefore: "warning" as const,
  severityAfter: "warning" as const,
  tagsAdded: [],
  firedAt: "2026-04-21T10:00:00Z",
};

describe("AlertEscalationTimeline", () => {
  it("renders 2 list items when 2 events are returned", async () => {
    mockListEvents.mockResolvedValue([
      { ...baseEvent, id: 1, levelIndex: 0 },
      { ...baseEvent, id: 2, levelIndex: 1 },
    ]);

    render(<AlertEscalationTimeline token="test-token" alertId={42} />);

    await waitFor(() => {
      // levelIndex 0 → "第 1 级", levelIndex 1 → "第 2 级"
      expect(screen.getByText(/第 1 级/)).toBeInTheDocument();
      expect(screen.getByText(/第 2 级/)).toBeInTheDocument();
    });
  });

  it("renders silenced-skip badge when integrationIds is empty", async () => {
    mockListEvents.mockResolvedValue([
      { ...baseEvent, id: 3, levelIndex: 0, integrationIds: [] },
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
