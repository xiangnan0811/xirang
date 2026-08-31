import { request } from "./core";
import type {
  Aggregation,
  ChartType,
  Dashboard,
  DashboardTimeRange,
  MetricDescriptor,
  Panel,
  PanelFilters,
  PanelQueryResult,
  PanelQuerySeries,
} from "@/types/domain";

export type DashboardInput = {
  name: string;
  description?: string;
  timeRange: DashboardTimeRange;
  customStart?: string | null;
  customEnd?: string | null;
  autoRefreshSeconds: number;
};

export type PanelInput = {
  title: string;
  chartType: ChartType;
  metric: string;
  filters: PanelFilters;
  aggregation: Aggregation;
  layoutX: number;
  layoutY: number;
  layoutW: number;
  layoutH: number;
};

export type LayoutItem = {
  id: number;
  layoutX: number;
  layoutY: number;
  layoutW: number;
  layoutH: number;
};

export type PanelQueryInput = {
  metric: string;
  filters: PanelFilters;
  aggregation: Aggregation;
  start: string;
  end: string;
};

type RawPanelFilters = {
  node_ids?: number[];
  task_ids?: number[];
};

type RawPanel = {
  id: number;
  dashboard_id?: number;
  title?: string;
  chart_type?: string;
  metric?: string;
  filters?: RawPanelFilters;
  aggregation?: string;
  layout_x?: number;
  layout_y?: number;
  layout_w?: number;
  layout_h?: number;
};

type RawDashboard = {
  id: number;
  owner_id?: number;
  name?: string;
  description?: string;
  time_range?: string;
  custom_start?: string | null;
  custom_end?: string | null;
  auto_refresh_seconds?: number;
  created_at?: string;
  updated_at?: string;
  panels?: RawPanel[];
};

type RawMetricDescriptor = {
  key: string;
  label?: string;
  family?: string;
  default_aggregation?: string;
  supported_aggregations?: string[];
};

type RawPanelQueryResult = {
  series?: PanelQuerySeries[];
  step_seconds?: number;
  truncated?: boolean;
};

function mapTimeRange(raw?: string): DashboardTimeRange {
  if (raw === "1h" || raw === "6h" || raw === "24h" || raw === "7d" || raw === "custom") {
    return raw;
  }
  return "1h";
}

function mapChartType(raw?: string): ChartType {
  if (raw === "line" || raw === "area" || raw === "bar" || raw === "number" || raw === "table") {
    return raw;
  }
  return "line";
}

function mapAggregation(raw?: string): Aggregation {
  if (raw === "avg" || raw === "max" || raw === "min" || raw === "sum" || raw === "p50" || raw === "p95" || raw === "p99") {
    return raw;
  }
  return "avg";
}

function mapFilters(raw?: RawPanelFilters): PanelFilters {
  return {
    nodeIds: raw?.node_ids,
    taskIds: raw?.task_ids,
  };
}

function mapPanel(row: RawPanel): Panel {
  return {
    id: row.id,
    dashboardId: Number(row.dashboard_id) || 0,
    title: String(row.title ?? ""),
    chartType: mapChartType(row.chart_type),
    metric: String(row.metric ?? ""),
    filters: mapFilters(row.filters),
    aggregation: mapAggregation(row.aggregation),
    layoutX: Number(row.layout_x) || 0,
    layoutY: Number(row.layout_y) || 0,
    layoutW: Number(row.layout_w) || 0,
    layoutH: Number(row.layout_h) || 0,
  };
}

function mapDashboard(row: RawDashboard): Dashboard {
  return {
    id: row.id,
    ownerId: Number(row.owner_id) || 0,
    name: String(row.name ?? ""),
    description: String(row.description ?? ""),
    timeRange: mapTimeRange(row.time_range),
    customStart: row.custom_start ?? null,
    customEnd: row.custom_end ?? null,
    autoRefreshSeconds: Number(row.auto_refresh_seconds) || 0,
    createdAt: String(row.created_at ?? ""),
    updatedAt: String(row.updated_at ?? ""),
    panels: Array.isArray(row.panels) ? row.panels.map(mapPanel) : undefined,
  };
}

function mapMetric(row: RawMetricDescriptor): MetricDescriptor {
  return {
    key: row.key,
    label: String(row.label ?? row.key),
    family: row.family === "task" ? "task" : "node",
    defaultAggregation: mapAggregation(row.default_aggregation),
    supportedAggregations: Array.isArray(row.supported_aggregations)
      ? row.supported_aggregations.map(mapAggregation)
      : [mapAggregation(row.default_aggregation)],
  };
}

function toDashboardWire(input: DashboardInput) {
  return {
    name: input.name,
    description: input.description,
    time_range: input.timeRange,
    custom_start: input.customStart,
    custom_end: input.customEnd,
    auto_refresh_seconds: input.autoRefreshSeconds,
  };
}

function toPanelWire(input: PanelInput) {
  return {
    title: input.title,
    chart_type: input.chartType,
    metric: input.metric,
    filters: {
      node_ids: input.filters.nodeIds,
      task_ids: input.filters.taskIds,
    },
    aggregation: input.aggregation,
    layout_x: input.layoutX,
    layout_y: input.layoutY,
    layout_w: input.layoutW,
    layout_h: input.layoutH,
  };
}

function toLayoutWire(items: LayoutItem[]) {
  return items.map((item) => ({
    id: item.id,
    layout_x: item.layoutX,
    layout_y: item.layoutY,
    layout_w: item.layoutW,
    layout_h: item.layoutH,
  }));
}

function toQueryWire(input: PanelQueryInput) {
  return {
    metric: input.metric,
    filters: {
      node_ids: input.filters.nodeIds,
      task_ids: input.filters.taskIds,
    },
    aggregation: input.aggregation,
    start: input.start,
    end: input.end,
  };
}

export function createDashboardsApi() {
  return {
    async listDashboards(token: string, options?: { signal?: AbortSignal }): Promise<Dashboard[]> {
      const rows = await request<RawDashboard[]>("/dashboards", { token, signal: options?.signal });
      return (rows ?? []).map(mapDashboard);
    },

    async getDashboard(token: string, id: number, options?: { signal?: AbortSignal }): Promise<Dashboard> {
      const row = await request<RawDashboard>(`/dashboards/${id}`, { token, signal: options?.signal });
      return mapDashboard(row);
    },

    async createDashboard(token: string, input: DashboardInput): Promise<Dashboard> {
      const row = await request<RawDashboard>("/dashboards", { token, method: "POST", body: toDashboardWire(input) });
      return mapDashboard(row);
    },

    async updateDashboard(token: string, id: number, input: DashboardInput): Promise<Dashboard> {
      const row = await request<RawDashboard>(`/dashboards/${id}`, { token, method: "PATCH", body: toDashboardWire(input) });
      return mapDashboard(row);
    },

    async deleteDashboard(token: string, id: number): Promise<{ deleted: boolean }> {
      return request<{ deleted: boolean }>(`/dashboards/${id}`, { token, method: "DELETE" });
    },

    async addPanel(token: string, dashboardID: number, input: PanelInput): Promise<Panel> {
      const row = await request<RawPanel>(`/dashboards/${dashboardID}/panels`, { token, method: "POST", body: toPanelWire(input) });
      return mapPanel(row);
    },

    async updatePanel(token: string, dashboardID: number, panelID: number, input: PanelInput): Promise<Panel> {
      const row = await request<RawPanel>(`/dashboards/${dashboardID}/panels/${panelID}`, { token, method: "PATCH", body: toPanelWire(input) });
      return mapPanel(row);
    },

    async deletePanel(token: string, dashboardID: number, panelID: number): Promise<{ deleted: boolean }> {
      return request<{ deleted: boolean }>(`/dashboards/${dashboardID}/panels/${panelID}`, {
        token, method: "DELETE",
      });
    },

    async updateLayout(token: string, dashboardID: number, items: LayoutItem[]): Promise<{ updated: number }> {
      return request<{ updated: number }>(`/dashboards/${dashboardID}/panels/layout`, {
        token, method: "PUT", body: { items: toLayoutWire(items) },
      });
    },

    async queryPanel(token: string, input: PanelQueryInput, options?: { signal?: AbortSignal }): Promise<PanelQueryResult> {
      const row = await request<RawPanelQueryResult>("/dashboards/panel-query", {
        token, method: "POST", body: toQueryWire(input), signal: options?.signal,
      });
      return {
        series: Array.isArray(row.series) ? row.series : [],
        stepSeconds: Number(row.step_seconds) || 0,
        truncated: row.truncated,
      };
    },

    async listMetrics(token: string, options?: { signal?: AbortSignal }): Promise<MetricDescriptor[]> {
      const rows = await request<RawMetricDescriptor[]>("/dashboards/metrics", { token, signal: options?.signal });
      return (rows ?? []).map(mapMetric);
    },
  };
}
