import type {
  HeaderKV,
  HttpMethod,
  NewServiceMonitorInput,
  ServiceMonitorView,
  StatusPageItem,
} from "@/types/domain";
import { headersToJSON } from "@/lib/service-monitor-headers";
import { request } from "./core";

type RawServiceMonitor = {
  id: number;
  name: string;
  description: string;
  type: string;
  target: string;
  interval_seconds: number;
  timeout_seconds: number;
  http_method: string;
  http_expected_status: number;
  http_headers: string;
  enabled: boolean;
  last_status: string;
  uptime_pct: number;
  last_checked_at: string | null;
  created_at: string;
  updated_at: string;
};

type RawNewServiceMonitorInput = {
  name: string;
  description?: string;
  type: "http" | "tcp";
  target: string;
  interval_seconds?: number;
  timeout_seconds?: number;
  http_method?: string;
  http_expected_status?: number;
  http_headers?: string;
  enabled?: boolean;
};

type RawStatusPageItem = {
  name: string;
  type: string;
  status: string;
  uptime_pct: number;
  last_checked_at: string | null;
};

const BASE_PATH = "/service-monitors";

const HTTP_METHODS: readonly HttpMethod[] = ["GET", "POST", "HEAD"];

function normalizeHttpMethod(value: string | undefined): HttpMethod {
  return HTTP_METHODS.includes((value ?? "").toUpperCase() as HttpMethod)
    ? ((value as string).toUpperCase() as HttpMethod)
    : "GET";
}

function safeParseHeaders(raw: string | undefined): HeaderKV[] {
  if (!raw || raw === "{}") return [];
  try {
    const obj = JSON.parse(raw);
    if (obj && typeof obj === "object") {
      return Object.entries(obj as Record<string, unknown>).map(
        ([k, v]) => ({ key: k, value: String(v) })
      );
    }
  } catch {
    // 坏 JSON：退化为空，避免整条监控解析失败
  }
  return [];
}

function mapServiceMonitor(raw: RawServiceMonitor): ServiceMonitorView {
  return {
    id: raw.id,
    name: raw.name,
    description: raw.description ?? "",
    type: raw.type === "tcp" ? "tcp" : "http",
    target: raw.target,
    intervalSeconds: Number(raw.interval_seconds) || 0,
    timeoutSeconds: Number(raw.timeout_seconds) || 0,
    httpMethod: normalizeHttpMethod(raw.http_method),
    httpExpectedStatus: Number(raw.http_expected_status) || 0,
    httpHeaderList: safeParseHeaders(raw.http_headers),
    enabled: Boolean(raw.enabled),
    lastStatus: (["up", "down", "unknown"].includes(raw.last_status) ? raw.last_status : "unknown") as ServiceMonitorView["lastStatus"],
    uptimePct: Number(raw.uptime_pct) || 0,
    lastCheckedAt: raw.last_checked_at ?? null,
    createdAt: raw.created_at,
    updatedAt: raw.updated_at,
  };
}

function mapStatusPageItem(raw: RawStatusPageItem): StatusPageItem {
  return {
    name: raw.name,
    type: raw.type,
    status: (["up", "down", "unknown"].includes(raw.status) ? raw.status : "unknown") as StatusPageItem["status"],
    uptimePct: Number(raw.uptime_pct) || 0,
    lastCheckedAt: raw.last_checked_at ?? null,
  };
}

function toRawInput(input: NewServiceMonitorInput): RawNewServiceMonitorInput {
  const raw: RawNewServiceMonitorInput = {
    name: input.name,
    description: input.description,
    type: input.type,
    target: input.target,
    interval_seconds: input.intervalSeconds,
    timeout_seconds: input.timeoutSeconds,
    http_method: input.httpMethod,
    http_expected_status: input.httpExpectedStatus,
    http_headers: input.httpHeaderList ? headersToJSON(input.httpHeaderList) : "{}",
    enabled: input.enabled,
  };
  return raw;
}

export function createServiceMonitorsApi() {
  return {
    async list(token: string, signal?: AbortSignal): Promise<ServiceMonitorView[]> {
      const raw = (await request<RawServiceMonitor[]>(BASE_PATH, { token, signal })) ?? [];
      return raw.map(mapServiceMonitor);
    },

    async get(token: string, id: number, signal?: AbortSignal): Promise<ServiceMonitorView> {
      const raw = await request<RawServiceMonitor>(`${BASE_PATH}/${id}`, { token, signal });
      return mapServiceMonitor(raw);
    },

    async create(token: string, input: NewServiceMonitorInput, signal?: AbortSignal): Promise<ServiceMonitorView> {
      const raw = await request<RawServiceMonitor>(BASE_PATH, {
        method: "POST",
        body: toRawInput(input),
        token,
        signal,
      });
      return mapServiceMonitor(raw);
    },

    async update(token: string, id: number, input: NewServiceMonitorInput, signal?: AbortSignal): Promise<ServiceMonitorView> {
      const raw = await request<RawServiceMonitor>(`${BASE_PATH}/${id}`, {
        method: "PUT",
        body: toRawInput(input),
        token,
        signal,
      });
      return mapServiceMonitor(raw);
    },

    async delete(token: string, id: number, signal?: AbortSignal): Promise<void> {
      await request<void>(`${BASE_PATH}/${id}`, { method: "DELETE", token, signal });
    },

    /** Public endpoint — no auth required. */
    async getStatusPage(signal?: AbortSignal): Promise<StatusPageItem[]> {
      const raw = (await request<RawStatusPageItem[]>("/status-page", { signal })) ?? [];
      return raw.map(mapStatusPageItem);
    },
  };
}
