import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AnomalyEventRow from "./anomaly-event-row";
import type { AnomalyEvent } from "@/types/domain";

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      const map: Record<string, string> = {
        "anomaly.detector.ewma": "基线异常",
        "anomaly.detector.disk_forecast": "磁盘预测",
        "anomaly.severity.warning": "告警",
        "anomaly.severity.critical": "严重",
        "anomaly.extra.sigmaSuffix": "σ",
        "anomaly.extra.forecastPrefix": `预计 ${opts?.days ?? "?"} 天爆满`,
      };
      return map[key] ?? key;
    },
  }),
}));

const baseEvent: AnomalyEvent = {
  id: 1,
  node_id: 10,
  detector: "ewma",
  metric: "cpu_percent",
  severity: "warning",
  observed_value: 95.5,
  baseline_value: 30.2,
  sigma: 3.14,
  forecast_days: null,
  alert_id: null,
  raised_alert: true,
  fired_at: "2026-04-21T10:00:00Z",
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
  it("renders EWMA event with sigma, baseline and observed values", () => {
    renderRow(baseEvent);

    expect(screen.getByText("基线异常")).toBeInTheDocument();
    expect(screen.getByText("cpu_percent")).toBeInTheDocument();
    // baseline → observed
    expect(screen.getByText("30.20 → 95.50")).toBeInTheDocument();
    // sigma extra
    expect(screen.getByText("3.14σ")).toBeInTheDocument();
  });

  it("renders disk forecast event with forecast_days text", () => {
    const event: AnomalyEvent = {
      ...baseEvent,
      id: 2,
      detector: "disk_forecast",
      metric: "disk_used_percent",
      sigma: null,
      forecast_days: 5.3,
    };
    renderRow(event);

    expect(screen.getByText("磁盘预测")).toBeInTheDocument();
    expect(screen.getByText(/5\.3.*天爆满|预计.*5\.3/)).toBeInTheDocument();
  });

  it("renders alert link when alert_id is set", () => {
    const event: AnomalyEvent = { ...baseEvent, id: 3, alert_id: 42 };
    renderRow(event);

    const link = screen.getByTestId("anomaly-alert-link-3");
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute("href", "/n?alert=42");
  });

  it("does not render alert link when alert_id is null", () => {
    renderRow({ ...baseEvent, id: 4, alert_id: null });

    expect(screen.queryByTestId("anomaly-alert-link-4")).not.toBeInTheDocument();
  });
});
