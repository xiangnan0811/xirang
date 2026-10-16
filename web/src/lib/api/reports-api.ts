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

export type ReportSchedulePreviewNoMatchReason =
  | "unsupported_month"
  | "unmatchable_field"
  | "horizon_exhausted";

// Times keep the server's RFC3339 strings with their offsets; they are never
// converted to the browser time zone. Validation parses them only to epochs.
export type ReportSchedulePreview = {
  evaluatedAt: string;
  expiresAt: string;
  timezone: string;
  searchUntil: string;
} & (
  | { status: "match"; reason: ""; windowStart: string; windowEnd: string }
  | {
      status: "no_match";
      reason: ReportSchedulePreviewNoMatchReason;
      windowStart: null;
      windowEnd: null;
    }
);

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

const INVALID_SCHEDULE_PREVIEW_MESSAGE = "Invalid report schedule preview response";
const PREVIEW_MAX_TTL_MS = 60_000;
const PREVIEW_WINDOW_MS = 60_000;
const NO_MATCH_REASONS: Record<ReportSchedulePreviewNoMatchReason, true> = {
  unsupported_month: true,
  unmatchable_field: true,
  horizon_exhausted: true,
};
const RFC3339_WITH_OFFSET =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(?:Z|([+-])(\d{2}):(\d{2}))$/;

function invalidSchedulePreview(): Error {
  return new Error(INVALID_SCHEDULE_PREVIEW_MESSAGE);
}

// Returns epoch milliseconds for an RFC3339 timestamp with an explicit offset,
// rejecting impossible calendar/clock fields instead of letting Date normalize them.
function parseRfc3339Instant(value: unknown): number {
  if (typeof value !== "string") throw invalidSchedulePreview();
  const match = RFC3339_WITH_OFFSET.exec(value);
  if (!match) throw invalidSchedulePreview();
  const [, y, mo, d, h, mi, s, fraction, sign, offH, offM] = match;
  const year = Number(y);
  const month = Number(mo);
  const day = Number(d);
  const hour = Number(h);
  const minute = Number(mi);
  const second = Number(s);
  const millis = fraction ? Number(fraction.slice(0, 3).padEnd(3, "0")) : 0;
  if (hour > 23 || minute > 59 || second > 59) throw invalidSchedulePreview();
  const date = new Date(0);
  date.setUTCFullYear(year, month - 1, day);
  date.setUTCHours(hour, minute, second, millis);
  if (
    date.getUTCFullYear() !== year ||
    date.getUTCMonth() !== month - 1 ||
    date.getUTCDate() !== day
  ) {
    throw invalidSchedulePreview();
  }
  let offsetMinutes = 0;
  if (sign) {
    const offsetHours = Number(offH);
    const offsetMins = Number(offM);
    if (offsetHours > 23 || offsetMins > 59) throw invalidSchedulePreview();
    offsetMinutes = (sign === "-" ? -1 : 1) * (offsetHours * 60 + offsetMins);
  }
  const epoch = date.getTime() - offsetMinutes * 60_000;
  if (!Number.isFinite(epoch)) throw invalidSchedulePreview();
  return epoch;
}

function mapReportSchedulePreview(raw: unknown): ReportSchedulePreview {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) throw invalidSchedulePreview();
  const row = raw as Record<string, unknown>;

  const evaluatedAt = row.evaluated_at;
  const expiresAt = row.expires_at;
  const searchUntil = row.search_until;
  const timezone = row.timezone;
  const evaluatedMs = parseRfc3339Instant(evaluatedAt);
  const expiresMs = parseRfc3339Instant(expiresAt);
  const searchUntilMs = parseRfc3339Instant(searchUntil);
  if (typeof timezone !== "string" || timezone.trim() === "") throw invalidSchedulePreview();
  if (!(evaluatedMs < expiresMs && expiresMs <= evaluatedMs + PREVIEW_MAX_TTL_MS)) {
    throw invalidSchedulePreview();
  }
  if (!(searchUntilMs > evaluatedMs)) throw invalidSchedulePreview();

  const base = {
    evaluatedAt: evaluatedAt as string,
    expiresAt: expiresAt as string,
    timezone,
    searchUntil: searchUntil as string,
  };

  if (row.status === "match") {
    if (row.reason !== "") throw invalidSchedulePreview();
    const windowStart = row.window_start;
    const windowEnd = row.window_end;
    const startMs = parseRfc3339Instant(windowStart);
    const endMs = parseRfc3339Instant(windowEnd);
    if (endMs - startMs !== PREVIEW_WINDOW_MS || !(endMs > evaluatedMs) || !(startMs < searchUntilMs)) {
      throw invalidSchedulePreview();
    }
    return {
      ...base,
      status: "match",
      reason: "",
      windowStart: windowStart as string,
      windowEnd: windowEnd as string,
    };
  }

  if (row.status === "no_match") {
    const reason = row.reason;
    if (typeof reason !== "string" || !Object.prototype.hasOwnProperty.call(NO_MATCH_REASONS, reason)) {
      throw invalidSchedulePreview();
    }
    if (row.window_start !== null || row.window_end !== null) throw invalidSchedulePreview();
    return {
      ...base,
      status: "no_match",
      reason: reason as ReportSchedulePreviewNoMatchReason,
      windowStart: null,
      windowEnd: null,
    };
  }

  throw invalidSchedulePreview();
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

    previewSchedule: async (
      token: string,
      cron: string,
      options: { signal?: AbortSignal } = {},
    ): Promise<ReportSchedulePreview> => {
      const query = new URLSearchParams({ cron });
      const raw = await request<unknown>(`/report-configs/schedule-preview?${query.toString()}`, {
        method: "GET",
        token,
        signal: options.signal,
      });
      // A response that arrives after cancellation must not reach the caller.
      options.signal?.throwIfAborted();
      return mapReportSchedulePreview(raw);
    },
  };
}
