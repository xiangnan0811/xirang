import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AnomalyEventRow from "./anomaly-event-row";
import type { AnomalyEvent } from "@/types/domain";

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string) => {
      const map: Record<string, string> = {
        "anomaly.detector.snapshot_diff": "快照差异",
        "anomaly.severity.warning": "告警",
        "anomaly.severity.critical": "严重",
        "anomaly.extra.sigmaSuffix": "σ",
      };
      return map[key] ?? key;
    },
  }),
}));

const baseEvent: AnomalyEvent = {
  id: 1,
  nodeId: 10,
  detector: "snapshot_diff",
  metric: "snapshot_churn",
  severity: "warning",
  observedValue: 40,
  baselineValue: 8,
  sigma: 3.14,
  alertId: null,
  raisedAlert: true,
  firedAt: "2026-04-21T10:00:00Z",
};

function renderRow(event: AnomalyEvent) {
  return render(
    <MemoryRouter>
      <table>
        <tbody>
          <AnomalyEventRow event={event} />
        </tbody>
      </table>
    </MemoryRouter>,
  );
}

describe("AnomalyEventRow", () => {
  it("renders snapshot diff with sigma, baseline and observed values", () => {
    renderRow(baseEvent);

    expect(screen.getByText("快照差异")).toBeInTheDocument();
    expect(screen.getByText("snapshot_churn")).toBeInTheDocument();
    expect(screen.getByText("8.00 → 40.00")).toBeInTheDocument();
    expect(screen.getByText("3.14σ")).toBeInTheDocument();
  });

  it("omits sigma when the snapshot event has no deviation", () => {
    renderRow({
      ...baseEvent,
      id: 2,
      metric: "ransomware_pattern",
      severity: "critical",
      sigma: null,
    });

    expect(screen.getByText("快照差异")).toBeInTheDocument();
    expect(screen.getByText("ransomware_pattern")).toBeInTheDocument();
    expect(screen.queryByText(/σ$/)).not.toBeInTheDocument();
  });

  it("renders alert link when alertId is set", () => {
    const event: AnomalyEvent = { ...baseEvent, id: 3, alertId: 42 };
    renderRow(event);

    const link = screen.getByTestId("anomaly-alert-link-3");
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute("href", "/app/notifications?alert=42");
  });

  it("does not render alert link when alertId is null", () => {
    renderRow({ ...baseEvent, id: 4, alertId: null });

    expect(screen.queryByTestId("anomaly-alert-link-4")).not.toBeInTheDocument();
  });
});
