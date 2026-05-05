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
} from "@/types/domain";

export type DashboardInput = {
  name: string;
  description?: string;
  time_range: DashboardTimeRange;
  custom_start?: string | null;
  custom_end?: string | null;
  auto_refresh_seconds: number;
};

export type PanelInput = {
  title: string;
  chart_type: ChartType;
  metric: string;
  filters: PanelFilters;
  aggregation: Aggregation;
  layout_x: number;
  layout_y: number;
  layout_w: number;
  layout_h: number;
};

export type LayoutItem = {
  id: number;
  layout_x: number;
  layout_y: number;
  layout_w: number;
  layout_h: number;
};

export type PanelQueryInput = {
  metric: string;
  filters: PanelFilters;
  aggregation: Aggregation;
  start: string;
  end: string;
};

export function createDashboardsApi() {
  return {
    async listDashboards(token: string, options?: { signal?: AbortSignal }): Promise<Dashboard[]> {
      return request<Dashboard[]>("/dashboards", { token, signal: options?.signal });
    },

    async getDashboard(token: string, id: number, options?: { signal?: AbortSignal }): Promise<Dashboard> {
      return request<Dashboard>(`/dashboards/${id}`, { token, signal: options?.signal });
    },

    async createDashboard(token: string, input: DashboardInput): Promise<Dashboard> {
      return request<Dashboard>("/dashboards", { token, method: "POST", body: input });
    },

    async updateDashboard(token: string, id: number, input: DashboardInput): Promise<Dashboard> {
      return request<Dashboard>(`/dashboards/${id}`, { token, method: "PATCH", body: input });
    },

    async deleteDashboard(token: string, id: number): Promise<{ deleted: boolean }> {
      return request<{ deleted: boolean }>(`/dashboards/${id}`, { token, method: "DELETE" });
    },

    async addPanel(token: string, dashboardID: number, input: PanelInput): Promise<Panel> {
      return request<Panel>(`/dashboards/${dashboardID}/panels`, { token, method: "POST", body: input });
    },

    async updatePanel(token: string, dashboardID: number, panelID: number, input: PanelInput): Promise<Panel> {
      return request<Panel>(`/dashboards/${dashboardID}/panels/${panelID}`, { token, method: "PATCH", body: input });
    },

    async deletePanel(token: string, dashboardID: number, panelID: number): Promise<{ deleted: boolean }> {
      return request<{ deleted: boolean }>(`/dashboards/${dashboardID}/panels/${panelID}`, {
        token, method: "DELETE",
      });
    },

    async updateLayout(token: string, dashboardID: number, items: LayoutItem[]): Promise<{ updated: number }> {
      return request<{ updated: number }>(`/dashboards/${dashboardID}/panels/layout`, {
        token, method: "PUT", body: { items },
      });
    },

    async queryPanel(token: string, input: PanelQueryInput, options?: { signal?: AbortSignal }): Promise<PanelQueryResult> {
      return request<PanelQueryResult>("/dashboards/panel-query", {
        token, method: "POST", body: input, signal: options?.signal,
      });
    },

    async listMetrics(token: string, options?: { signal?: AbortSignal }): Promise<MetricDescriptor[]> {
      return request<MetricDescriptor[]>("/dashboards/metrics", { token, signal: options?.signal });
    },
  };
}
