import { request } from "./core";
import { finiteNumber, nullableFiniteNumber } from "./number-utils";

export type ReportScopeType = "all" | "tag" | "node_ids";
export type ReportPeriod = "weekly" | "monthly";

export type ReportConfig = {
  id: number;
  name: string;
  scopeType: ReportScopeType;
  scopeValue: string;
  period: ReportPeriod;
  cron: string;
  integrationIds: number[];
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
};

export type ReportFailure = {
  nodeName: string;
  taskName: string;
  count: number;
  lastErr: string;
};

export type Report = {
  id: number;
  configId: number;
  config?: ReportConfig;
  periodStart: string;
  periodEnd: string;
  totalRuns: number;
  successRuns: number;
  failedRuns: number;
  successRate: number;
  avgDurationMs: number;
  topFailures: ReportFailure[];
  diskTrend: unknown[];
  actualRpoMinutes?: number | null;
  actualRtoMinutes?: number | null;
  rpoCompliant?: boolean | null;
  rtoCompliant?: boolean | null;
  generatedAt: string;
  createdAt: string;
};

export type NewReportConfigInput = {
  name: string;
  scopeType: ReportScopeType;
  scopeValue: string;
  period: ReportPeriod;
  cron: string;
  integrationIds: number[];
  enabled: boolean;
};

type RawReportConfig = {
  id?: unknown;
  name?: unknown;
  scope_type?: unknown;
  scope_value?: unknown;
  period?: unknown;
  cron?: unknown;
  integration_ids?: unknown;
  enabled?: unknown;
  created_at?: unknown;
  updated_at?: unknown;
};

type RawReport = {
  id?: unknown;
  config_id?: unknown;
  config?: RawReportConfig;
  period_start?: unknown;
  period_end?: unknown;
  total_runs?: unknown;
  success_runs?: unknown;
  failed_runs?: unknown;
  success_rate?: unknown;
  avg_duration_ms?: unknown;
  top_failures?: unknown;
  disk_trend?: unknown;
  actual_rpo_minutes?: unknown;
  actual_rto_minutes?: unknown;
  rpo_compliant?: unknown;
  rto_compliant?: unknown;
  generated_at?: unknown;
  created_at?: unknown;
};

function mapScopeType(raw: unknown): ReportScopeType {
  if (raw === "all" || raw === "tag" || raw === "node_ids") return raw;
  return "all";
}

function mapPeriod(raw: unknown): ReportPeriod {
  return raw === "monthly" ? "monthly" : "weekly";
}

function parseJsonArray(raw: unknown): unknown[] {
  if (Array.isArray(raw)) return raw;
  if (typeof raw === "string" && raw.trim()) {
    try {
      const parsed: unknown = JSON.parse(raw);
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  }
  return [];
}

export function parseIntegrationIds(raw: unknown): number[] {
  return parseJsonArray(raw)
    .map((value) => finiteNumber(value))
    .filter((value) => value > 0);
}

export function mapReportConfig(row: RawReportConfig | null | undefined): ReportConfig {
  return {
    id: finiteNumber(row?.id),
    name: String(row?.name ?? ""),
    scopeType: mapScopeType(row?.scope_type),
    scopeValue: String(row?.scope_value ?? ""),
    period: mapPeriod(row?.period),
    cron: String(row?.cron ?? ""),
    integrationIds: parseIntegrationIds(row?.integration_ids),
    enabled: Boolean(row?.enabled),
    createdAt: String(row?.created_at ?? ""),
    updatedAt: String(row?.updated_at ?? ""),
  };
}

export function mapReport(row: RawReport | null | undefined): Report {
  return {
    id: finiteNumber(row?.id),
    configId: finiteNumber(row?.config_id),
    config: row?.config ? mapReportConfig(row.config) : undefined,
    periodStart: String(row?.period_start ?? ""),
    periodEnd: String(row?.period_end ?? ""),
    totalRuns: finiteNumber(row?.total_runs),
    successRuns: finiteNumber(row?.success_runs),
    failedRuns: finiteNumber(row?.failed_runs),
    successRate: finiteNumber(row?.success_rate),
    avgDurationMs: finiteNumber(row?.avg_duration_ms),
    topFailures: parseJsonArray(row?.top_failures).map((item) => {
      const failure = item && typeof item === "object" ? (item as Record<string, unknown>) : {};
      return {
        nodeName: String(failure.node_name ?? ""),
        taskName: String(failure.task_name ?? ""),
        count: finiteNumber(failure.count),
        lastErr: String(failure.last_err ?? ""),
      };
    }),
    diskTrend: parseJsonArray(row?.disk_trend),
    actualRpoMinutes: nullableFiniteNumber(row?.actual_rpo_minutes),
    actualRtoMinutes: nullableFiniteNumber(row?.actual_rto_minutes),
    rpoCompliant: row?.rpo_compliant == null ? null : Boolean(row.rpo_compliant),
    rtoCompliant: row?.rto_compliant == null ? null : Boolean(row.rto_compliant),
    generatedAt: String(row?.generated_at ?? ""),
    createdAt: String(row?.created_at ?? ""),
  };
}

function toReportConfigWire(input: NewReportConfigInput | Partial<NewReportConfigInput>) {
  const body: Record<string, unknown> = {};
  if (input.name !== undefined) body.name = input.name;
  if (input.scopeType !== undefined) body.scope_type = input.scopeType;
  if (input.scopeValue !== undefined) body.scope_value = input.scopeValue;
  if (input.period !== undefined) body.period = input.period;
  if (input.cron !== undefined) body.cron = input.cron;
  if (input.integrationIds !== undefined) body.integration_ids = input.integrationIds;
  if (input.enabled !== undefined) body.enabled = input.enabled;
  return body;
}

export function createReportsApi() {
  return {
    listConfigs: async (token: string): Promise<ReportConfig[]> => {
      const rows = await request<RawReportConfig[]>("/report-configs", { method: "GET", token });
      return Array.isArray(rows) ? rows.map(mapReportConfig) : [];
    },

    createConfig: async (token: string, input: NewReportConfigInput): Promise<ReportConfig> => {
      const row = await request<RawReportConfig>("/report-configs", {
        method: "POST",
        token,
        body: toReportConfigWire(input),
      });
      return mapReportConfig(row);
    },

    updateConfig: async (
      token: string,
      id: number,
      input: Partial<NewReportConfigInput>,
    ): Promise<ReportConfig> => {
      const row = await request<RawReportConfig>(`/report-configs/${id}`, {
        method: "PUT",
        token,
        body: toReportConfigWire(input),
      });
      return mapReportConfig(row);
    },

    deleteConfig: (token: string, id: number) =>
      request<unknown>(`/report-configs/${id}`, { method: "DELETE", token }),

    generateNow: async (token: string, id: number): Promise<Report> => {
      const row = await request<RawReport>(`/report-configs/${id}/generate`, { method: "POST", token });
      return mapReport(row);
    },

    listReports: async (token: string, configId: number): Promise<Report[]> => {
      const rows = await request<RawReport[]>(`/report-configs/${configId}/reports`, { method: "GET", token });
      return Array.isArray(rows) ? rows.map(mapReport) : [];
    },

    getReport: async (token: string, id: number): Promise<Report> => {
      const row = await request<RawReport>(`/reports/${id}`, { method: "GET", token });
      return mapReport(row);
    },
  };
}
