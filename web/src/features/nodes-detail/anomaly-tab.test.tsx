import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AnomalyTab from "./anomaly-tab";
import type { AnomalyEvent } from "@/types/domain";

const { mockListNodeAnomalyEvents } = vi.hoisted(() => ({
  mockListNodeAnomalyEvents: vi.fn(),
}));

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listNodeAnomalyEvents: mockListNodeAnomalyEvents,
    },
  };
});

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string) => {
      const map: Record<string, string> = {
        "anomaly.tab.empty": "该节点尚无异常记录",
        "anomaly.table.firedAt": "时间",
        "anomaly.table.detector": "检测器",
        "anomaly.table.metric": "指标",
        "anomaly.table.severity": "严重度",
        "anomaly.table.baselineObserved": "基线 → 观测",
        "anomaly.table.extra": "附加",
        "anomaly.table.alert": "告警",
        "anomaly.detector.snapshot_diff": "快照差异",
        "anomaly.severity.warning": "告警",
        "anomaly.severity.critical": "严重",
        "anomaly.extra.sigmaSuffix": "σ",
        "anomaly.errors.loadFailed": "加载异常事件失败",
        "common.loading": "加载中...",
      };
      return map[key] ?? key;
    },
  }),
}));

const makeEvent = (overrides: Partial<AnomalyEvent> = {}): AnomalyEvent => ({
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
  ...overrides,
});

function renderTab() {
  return render(
    <MemoryRouter>
      <AnomalyTab nodeId={10} token="test-token" />
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
    expect(mockListNodeAnomalyEvents).toHaveBeenCalledWith("test-token", 10, { limit: 50 });
  });

  it("renders 2 event rows when API returns 2 events", async () => {
    mockListNodeAnomalyEvents.mockResolvedValue([
      makeEvent({ id: 1, metric: "snapshot_churn" }),
      makeEvent({ id: 2, metric: "ransomware_pattern", sigma: null }),
    ]);
    renderTab();
    await waitFor(() => {
      expect(screen.getByTestId("anomaly-tab")).toBeInTheDocument();
    });
    expect(screen.getByText("snapshot_churn")).toBeInTheDocument();
    expect(screen.getByText("ransomware_pattern")).toBeInTheDocument();
    expect(screen.getByText("3.14σ")).toBeInTheDocument();
  });

  it("skips loading when token is missing", () => {
    render(
      <MemoryRouter>
        <AnomalyTab nodeId={10} token={null} />
      </MemoryRouter>,
    );

    expect(mockListNodeAnomalyEvents).not.toHaveBeenCalled();
  });
});
