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
    listConfigs: (token: string) =>
      request<Envelope<ReportConfig[]>>("/report-configs", { method: "GET", token }).then(unwrapData),

    createConfig: (token: string, input: NewReportConfigInput) =>
      request<Envelope<ReportConfig>>("/report-configs", {
        method: "POST",
        token,
        body: input,
      }).then(unwrapData),

    updateConfig: (token: string, id: number, input: Partial<NewReportConfigInput>) =>
      request<Envelope<ReportConfig>>(`/report-configs/${id}`, {
        method: "PUT",
        token,
        body: input,
      }).then(unwrapData),

    deleteConfig: (token: string, id: number) =>
      request<Envelope<unknown>>(`/report-configs/${id}`, { method: "DELETE", token }),

    generateNow: (token: string, id: number) =>
      request<Envelope<Report>>(`/report-configs/${id}/generate`, { method: "POST", token }).then(unwrapData),

    listReports: (token: string, configId: number) =>
      request<Envelope<Report[]>>(`/report-configs/${configId}/reports`, { method: "GET", token }).then(unwrapData),

    getReport: (token: string, id: number) =>
      request<Envelope<Report>>(`/reports/${id}`, { method: "GET", token }).then(unwrapData),
  };
}
