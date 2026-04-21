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

export const listDashboards = (token: string) =>
  request<Dashboard[]>("/dashboards", { token });

export const getDashboard = (token: string, id: number, signal?: AbortSignal) =>
  request<Dashboard>(`/dashboards/${id}`, { token, signal });

export const createDashboard = (token: string, input: DashboardInput) =>
  request<Dashboard>("/dashboards", { token, method: "POST", body: input });

export const updateDashboard = (token: string, id: number, input: DashboardInput) =>
  request<Dashboard>(`/dashboards/${id}`, { token, method: "PATCH", body: input });

export const deleteDashboard = (token: string, id: number) =>
  request<{ deleted: boolean }>(`/dashboards/${id}`, { token, method: "DELETE" });

export const addPanel = (token: string, dashboardID: number, input: PanelInput) =>
  request<Panel>(`/dashboards/${dashboardID}/panels`, { token, method: "POST", body: input });

export const updatePanel = (token: string, dashboardID: number, panelID: number, input: PanelInput) =>
  request<Panel>(`/dashboards/${dashboardID}/panels/${panelID}`, { token, method: "PATCH", body: input });

export const deletePanel = (token: string, dashboardID: number, panelID: number) =>
  request<{ deleted: boolean }>(`/dashboards/${dashboardID}/panels/${panelID}`, {
    token, method: "DELETE",
  });

export const updateLayout = (token: string, dashboardID: number, items: LayoutItem[]) =>
  request<{ updated: number }>(`/dashboards/${dashboardID}/panels/layout`, {
    token, method: "PUT", body: { items },
  });

export const queryPanel = (token: string, input: PanelQueryInput, signal?: AbortSignal) =>
  request<PanelQueryResult>("/dashboards/panel-query", {
    token, method: "POST", body: input, signal,
  });

export const listMetrics = (token: string) =>
  request<MetricDescriptor[]>("/dashboards/metrics", { token });
