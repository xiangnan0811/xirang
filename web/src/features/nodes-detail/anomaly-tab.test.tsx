import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AnomalyTab from "./anomaly-tab";
import type { AnomalyEvent } from "@/types/domain";

const { mockListNodeAnomalyEvents } = vi.hoisted(() => ({
  mockListNodeAnomalyEvents: vi.fn(),
}));

vi.mock("@/lib/api/anomaly", () => ({
  listNodeAnomalyEvents: mockListNodeAnomalyEvents,
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      const map: Record<string, string> = {
        "anomaly.tab.empty": "该节点尚无异常记录",
        "anomaly.table.firedAt": "时间",
        "anomaly.table.detector": "检测器",
        "anomaly.table.metric": "指标",
        "anomaly.table.severity": "严重度",
        "anomaly.table.baselineObserved": "基线 → 观测",
        "anomaly.table.extra": "附加",
        "anomaly.table.alert": "告警",
        "anomaly.detector.ewma": "基线异常",
        "anomaly.detector.disk_forecast": "磁盘预测",
        "anomaly.severity.warning": "告警",
        "anomaly.severity.critical": "严重",
        "anomaly.extra.sigmaSuffix": "σ",
        "anomaly.extra.forecastPrefix": `预计 ${opts?.days ?? "?"} 天爆满`,
        "anomaly.errors.loadFailed": "加载异常事件失败",
        "common.loading": "加载中...",
      };
      return map[key] ?? key;
    },
  }),
}));

const makeEvent = (overrides: Partial<AnomalyEvent> = {}): AnomalyEvent => ({
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
  ...overrides,
});

function renderTab() {
  return render(
    <MemoryRouter>
      <AnomalyTab nodeId={10} />
    </MemoryRouter>,
  );
}

describe("AnomalyTab", () => {
  beforeEach(() => {
    mockListNodeAnomalyEvents.mockReset();
  });

  it("shows loading skeleton initially", () => {
    // Never resolves during test
    mockListNodeAnomalyEvents.mockReturnValue(new Promise(() => {}));
    renderTab();
    expect(screen.getByTestId("anomaly-tab-loading")).toBeInTheDocument();
  });

  it("shows empty state when no events returned", async () => {
    mockListNodeAnomalyEvents.mockResolvedValue([]);
    renderTab();
    await waitFor(() => {
      expect(screen.getByTestId("anomaly-tab-empty")).toBeInTheDocument();
    });
    expect(screen.getByText("该节点尚无异常记录")).toBeInTheDocument();
  });

  it("renders 2 event rows when API returns 2 events", async () => {
    mockListNodeAnomalyEvents.mockResolvedValue([
      makeEvent({ id: 1, metric: "cpu_percent" }),
      makeEvent({ id: 2, metric: "mem_percent" }),
    ]);
    renderTab();
    await waitFor(() => {
      expect(screen.getByTestId("anomaly-tab")).toBeInTheDocument();
    });
    expect(screen.getByText("cpu_percent")).toBeInTheDocument();
    expect(screen.getByText("mem_percent")).toBeInTheDocument();
  });

  it("renders different detector badges for ewma and disk_forecast events", async () => {
    mockListNodeAnomalyEvents.mockResolvedValue([
      makeEvent({ id: 1, detector: "ewma" }),
      makeEvent({ id: 2, detector: "disk_forecast", sigma: null, forecast_days: 5.0 }),
    ]);
    renderTab();
    await waitFor(() => {
      expect(screen.getByTestId("anomaly-tab")).toBeInTheDocument();
    });
    expect(screen.getByText("基线异常")).toBeInTheDocument();
    expect(screen.getByText("磁盘预测")).toBeInTheDocument();
  });
});
