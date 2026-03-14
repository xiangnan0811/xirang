import { request, unwrapData, type Envelope } from "./core";

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
    listConfigs: () =>
      request<Envelope<ReportConfig[]>>("/report-configs", { method: "GET" }).then(unwrapData),

    createConfig: (input: NewReportConfigInput) =>
      request<Envelope<ReportConfig>>("/report-configs", {
        method: "POST",
        body: JSON.stringify(input),
      }).then(unwrapData),

    updateConfig: (id: number, input: Partial<NewReportConfigInput>) =>
      request<Envelope<ReportConfig>>(`/report-configs/${id}`, {
        method: "PUT",
        body: JSON.stringify(input),
      }).then(unwrapData),

    deleteConfig: (id: number) =>
      request<Envelope<unknown>>(`/report-configs/${id}`, { method: "DELETE" }),

    generateNow: (id: number) =>
      request<Envelope<Report>>(`/report-configs/${id}/generate`, { method: "POST" }).then(unwrapData),

    listReports: (configId: number) =>
      request<Envelope<Report[]>>(`/report-configs/${configId}/reports`, { method: "GET" }).then(unwrapData),

    getReport: (id: number) =>
      request<Envelope<Report>>(`/reports/${id}`, { method: "GET" }).then(unwrapData),
  };
}
