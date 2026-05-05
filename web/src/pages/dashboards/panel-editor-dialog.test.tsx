import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { PanelEditorDialog } from "./panel-editor-dialog";
import type { MetricDescriptor, Panel } from "@/types/domain";

// ─── Mocks ───────────────────────────────────────────────────────

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listMetrics: vi.fn(),
      addPanel: vi.fn(),
      updatePanel: vi.fn(),
      queryPanel: vi.fn(),
    },
  };
});

vi.mock("@/lib/api/nodes-api", () => ({
  createNodesApi: vi.fn(() => ({
    getNodes: vi.fn().mockResolvedValue([
      { id: 1, name: "node-a" },
      { id: 2, name: "node-b" },
    ]),
  })),
}));

vi.mock("@/lib/api/tasks-api", () => ({
  createTasksApi: vi.fn(() => ({
    getTasks: vi.fn().mockResolvedValue([
      { id: 10, name: "task-x" },
    ]),
  })),
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

// recharts 在 jsdom 中需要 mock ResizeObserver
global.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

import { apiClient } from "@/lib/api/client";

const mockListMetrics = vi.mocked(apiClient.listMetrics);
const mockAddPanel = vi.mocked(apiClient.addPanel);
const mockUpdatePanel = vi.mocked(apiClient.updatePanel);
const mockQueryPanel = vi.mocked(apiClient.queryPanel);

// ─── 测试数据 ─────────────────────────────────────────────────────

const nodeCpuMetric: MetricDescriptor = {
  key: "node.cpu",
  label: "CPU 使用率",
  family: "node",
  default_aggregation: "avg",
  supported_aggregations: ["avg", "max", "min"],
};

const taskMetric: MetricDescriptor = {
  key: "task.success_rate",
  label: "任务成功率",
  family: "task",
  default_aggregation: "avg",
  supported_aggregations: ["avg"],
};

const mockMetrics: MetricDescriptor[] = [nodeCpuMetric, taskMetric];

const mockPanelData = {
  series: [{ name: "node-a", points: [{ ts: "2026-04-21T00:00:00Z", value: 50 }] }],
  step_seconds: 60,
};

const editPanel: Panel = {
  id: 42,
  dashboard_id: 1,
  title: "现有面板",
  chart_type: "bar",
  metric: "node.cpu",
  filters: { node_ids: [1] },
  aggregation: "max",
  layout_x: 0,
  layout_y: 0,
  layout_w: 6,
  layout_h: 4,
};

// ─── 渲染辅助 ─────────────────────────────────────────────────────

interface RenderProps {
  open?: boolean;
  panel?: Panel;
  onSaved?: (p: Panel) => void;
  onOpenChange?: (open: boolean) => void;
}

function renderDialog({
  open = true,
  panel,
  onSaved = vi.fn(),
  onOpenChange = vi.fn(),
}: RenderProps = {}) {
  return render(
    <I18nextProvider i18n={i18n}>
      <PanelEditorDialog
        open={open}
        onOpenChange={onOpenChange}
        dashboardID={1}
        start="2026-04-21T00:00:00Z"
        end="2026-04-21T01:00:00Z"
        panel={panel}
        onSaved={onSaved}
        token="test-token"
      />
    </I18nextProvider>
  );
}

// ─── 测试 ─────────────────────────────────────────────────────────

describe("PanelEditorDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockListMetrics.mockResolvedValue(mockMetrics);
    mockQueryPanel.mockResolvedValue(mockPanelData);
    mockAddPanel.mockResolvedValue({
      id: 99,
      dashboard_id: 1,
      title: "新面板",
      chart_type: "line",
      metric: "node.cpu",
      filters: {},
      aggregation: "avg",
      layout_x: 0,
      layout_y: 0,
      layout_w: 6,
      layout_h: 4,
    });
    mockUpdatePanel.mockResolvedValue({
      ...editPanel,
      title: "现有面板（更新）",
    });
  });

  it("创建模式：选择任务指标后，聚合选项更新为 task 支持的列表", async () => {
    renderDialog();

    // 等待指标列表加载（第一个指标 node.cpu 的 label 出现在 option 中）
    await waitFor(() => {
      expect(mockListMetrics).toHaveBeenCalled();
    });

    // 等待指标 select 渲染出 node.cpu 的选项
    await waitFor(() => {
      expect(screen.getByDisplayValue(/CPU 使用率/)).toBeInTheDocument();
    });

    // 找到显示 "CPU 使用率" 的 select（指标选择器）
    const user = userEvent.setup();
    const metricSelect = screen.getByDisplayValue(/CPU 使用率/);

    // 切换到 task.success_rate
    await user.selectOptions(metricSelect, "task.success_rate");

    // 聚合 select 应只剩 avg（task.success_rate 仅支持 avg）
    await waitFor(() => {
      const selects = screen.getAllByRole("combobox") as HTMLSelectElement[];
      // 找到当前值为 avg 且只有一个 option 的 select（聚合选择器）
      const aggSelect = selects.find(
        (s) => s.value === "avg" && s.options.length === 1
      );
      expect(aggSelect).toBeDefined();
      expect(aggSelect!.options[0].value).toBe("avg");
    });
  });

  it("创建模式：点击保存调用 addPanel，onSaved 收到返回的面板", async () => {
    const onSaved = vi.fn();
    const onOpenChange = vi.fn();
    renderDialog({ onSaved, onOpenChange });

    // 等待指标加载
    await waitFor(() => expect(mockListMetrics).toHaveBeenCalled());

    const user = userEvent.setup();

    // 填入标题
    const titleInput = screen.getByPlaceholderText(/面板标题/i);
    await user.clear(titleInput);
    await user.type(titleInput, "新面板");

    // 点击保存
    const saveBtn = screen.getByRole("button", { name: /保存/ });
    await user.click(saveBtn);

    await waitFor(() => {
      expect(mockAddPanel).toHaveBeenCalledWith(
        "test-token",
        1,
        expect.objectContaining({
          title: "新面板",
          chart_type: expect.any(String),
          metric: expect.any(String),
          aggregation: expect.any(String),
        })
      );
      expect(onSaved).toHaveBeenCalledWith(
        expect.objectContaining({ id: 99 })
      );
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });

  it("标题为空时，保存按钮被禁用", async () => {
    renderDialog();

    // 等待指标加载
    await waitFor(() => expect(mockListMetrics).toHaveBeenCalled());

    // 标题默认为空（创建模式）
    const saveBtn = screen.getByRole("button", { name: /保存/ });
    expect(saveBtn).toBeDisabled();
  });

  it("编辑模式：从 panel prop 回填字段", async () => {
    renderDialog({ panel: editPanel });

    await waitFor(() => expect(mockListMetrics).toHaveBeenCalled());

    // 标题已回填
    const titleInput = screen.getByPlaceholderText(/面板标题/i) as HTMLInputElement;
    await waitFor(() => {
      expect(titleInput.value).toBe("现有面板");
    });

    // 图表类型已回填为 bar
    const selects = screen.getAllByRole("combobox") as HTMLSelectElement[];
    const chartTypeSelect = selects.find((s) => s.value === "bar");
    expect(chartTypeSelect).toBeDefined();

    // 聚合已回填为 max
    const aggSelect = selects.find((s) => s.value === "max");
    expect(aggSelect).toBeDefined();
  });
});
