import type { AuditLogRecord } from "@/types/domain";
import { ApiError, fetchWithFallback, formatTime, request, type Envelope, unwrapData } from "./core";

type AuditLogResponse = {
  id: number;
  user_id: number;
  username: string;
  role: string;
  method: string;
  path: string;
  status_code: number;
  client_ip: string;
  user_agent: string;
  created_at: string;
};

function mapAuditLog(row: AuditLogResponse): AuditLogRecord {
  return {
    id: row.id,
    userId: row.user_id,
    username: row.username,
    role: row.role,
    method: row.method,
    path: row.path,
    statusCode: row.status_code,
    clientIP: row.client_ip,
    userAgent: row.user_agent,
    createdAt: formatTime(row.created_at)
  };
}

type AuditQueryOptions = {
  username?: string;
  role?: string;
  method?: string;
  path?: string;
  statusCode?: number;
  from?: string;
  to?: string;
  page?: number;
  pageSize?: number;
};

function buildAuditQuery(options?: AuditQueryOptions): URLSearchParams {
  const query = new URLSearchParams();
  if (options?.username?.trim()) {
    query.set("username", options.username.trim());
  }
  if (options?.role?.trim()) {
    query.set("role", options.role.trim());
  }
  if (options?.method?.trim()) {
    query.set("method", options.method.trim());
  }
  if (options?.path?.trim()) {
    query.set("path", options.path.trim());
  }
  if (options?.statusCode && Number.isFinite(options.statusCode)) {
    query.set("status_code", String(options.statusCode));
  }
  if (options?.from?.trim()) {
    query.set("from", options.from.trim());
  }
  if (options?.to?.trim()) {
    query.set("to", options.to.trim());
  }
  if (options?.page && Number.isFinite(options.page) && options.page > 0) {
    query.set("page", String(options.page));
  }
  if (options?.pageSize && Number.isFinite(options.pageSize) && options.pageSize > 0) {
    query.set("page_size", String(options.pageSize));
  }
  return query;
}

export function createAuditApi() {
  return {
    async getAuditLogs(
      token: string,
      options?: AuditQueryOptions
    ): Promise<{ items: AuditLogRecord[]; total: number; page: number; pageSize: number }> {
      const query = buildAuditQuery(options);
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<Envelope<AuditLogResponse[]> & { total?: number; page?: number; page_size?: number }>(
        `/audit-logs${suffix}`,
        {
          token
        }
      );
      const rows = unwrapData(payload) ?? [];
      return {
        items: rows.map((row) => mapAuditLog(row)),
        total: typeof payload.total === "number" ? payload.total : rows.length,
        page: typeof payload.page === "number" ? payload.page : 1,
        pageSize: typeof payload.page_size === "number" ? payload.page_size : rows.length,
      };
    },

    async exportAuditLogsCSV(
      token: string,
      options?: Omit<AuditQueryOptions, "offset">
    ): Promise<Blob> {
      const query = buildAuditQuery(options);
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const response = await fetchWithFallback(`/audit-logs/export${suffix}`, {
        method: "GET",
        headers: {
          Authorization: `Bearer ${token}`
        }
      });

      if (!response.ok) {
        const text = await response.text();
        let detail: unknown = text;
        if (text) {
          try {
            detail = JSON.parse(text);
          } catch {
            detail = text;
          }
        }
        throw new ApiError(response.status, `请求失败：${response.status}`, detail);
      }

      return response.blob();
    }
  };
}
