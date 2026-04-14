import { request } from "./core";

export type ReportConfig = {
  id: number;
  name: string;
  scope_type: "all" | "tag" | "node_ids";
  scope_value: string;
  period: "weekly" | "monthly";
  cron: string;
  integration_ids: string; // JSON array string
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type Report = {
  id: number;
  config_id: number;
  config?: ReportConfig;
  period_start: string;
  period_end: string;
  total_runs: number;
  success_runs: number;
  failed_runs: number;
  success_rate: number;
  avg_duration_ms: number;
  top_failures: string; // JSON array
  disk_trend: string;   // JSON array
  generated_at: string;
  created_at: string;
};

export type NewReportConfigInput = {
  name: string;
  scope_type: "all" | "tag" | "node_ids";
  scope_value: string;
  period: "weekly" | "monthly";
  cron: string;
  integration_ids: number[];
  enabled: boolean;
};

export function createReportsApi() {
  return {
    listConfigs: (token: string) =>
      request<ReportConfig[]>("/report-configs", { method: "GET", token }),

    createConfig: (token: string, input: NewReportConfigInput) =>
      request<ReportConfig>("/report-configs", {
        method: "POST",
        token,
        body: input,
      }),

    updateConfig: (token: string, id: number, input: Partial<NewReportConfigInput>) =>
      request<ReportConfig>(`/report-configs/${id}`, {
        method: "PUT",
        token,
        body: input,
      }),

    deleteConfig: (token: string, id: number) =>
      request<unknown>(`/report-configs/${id}`, { method: "DELETE", token }),

    generateNow: (token: string, id: number) =>
      request<Report>(`/report-configs/${id}/generate`, { method: "POST", token }),

    listReports: (token: string, configId: number) =>
      request<Report[]>(`/report-configs/${configId}/reports`, { method: "GET", token }),

    getReport: (token: string, id: number) =>
      request<Report>(`/reports/${id}`, { method: "GET", token }),
  };
}
