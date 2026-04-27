import type {
  AlertBulkRetryResult,
  AlertDeliveryRecord,
  AlertDeliveryRetryResult,
  AlertDeliveryStats,
  AlertRecord
} from "@/types/domain";
import i18n from "@/i18n";
import { formatTime, parseNumericId, request, type PaginatedEnvelope, unwrapPaginated } from "./core";

type AlertResponse = {
  id: number;
  node_id: number;
  node_name: string;
  task_id?: number | null;
  task_run_id?: number | null;
  slo_id?: number | null;
  policy_name?: string;
  severity: AlertRecord["severity"];
  status: AlertRecord["status"];
  error_code: string;
  message: string;
  retryable: boolean;
  triggered_at: string;
};

type AlertDeliveryResponse = {
  id: number;
  alert_id: number;
  integration_id: number;
  status: "sent" | "failed";
  error?: string;
  created_at: string;
  // retry columns (P5b Task 4)
  attempt_count?: number;
  next_retry_at?: string | null;
  last_error?: string | null;
};

type RetryAlertDeliveryResponse = {
  ok: boolean;
  message: string;
  delivery: AlertDeliveryResponse;
};

type RetryFailedDeliveriesResponse = {
  ok: boolean;
  message: string;
  total_failed: number;
  success_count: number;
  failed_count: number;
  new_deliveries: AlertDeliveryResponse[];
};

type DeliveryStatsIntegrationResponse = {
  integration_id: number;
  name: string;
  type: string;
  sent: number;
  failed: number;
};

type DeliveryStatsResponse = {
  window_hours: number;
  total_sent: number;
  total_failed: number;
  success_rate: number;
  by_integration: DeliveryStatsIntegrationResponse[];
};

function mapAlert(row: AlertResponse): AlertRecord {
  return {
    id: `alert-${row.id}`,
    nodeName: row.node_name,
    nodeId: row.node_id,
    taskId: row.task_id ?? null,
    taskRunId: row.task_run_id ?? null,
    sloId: row.slo_id ?? null,
    policyName: row.policy_name ?? i18n.t("notifications.nodeProbe"),
    severity: row.severity,
    status: row.status,
    errorCode: row.error_code,
    message: row.message,
    triggeredAt: formatTime(row.triggered_at),
    retryable: row.retryable
  };
}

function mapAlertDelivery(row: AlertDeliveryResponse): AlertDeliveryRecord {
  return {
    id: `delivery-${row.id}`,
    alertId: `alert-${row.alert_id}`,
    integrationId: `int-${row.integration_id}`,
    status: row.status === "failed" ? "failed" : "sent",
    error: row.error || undefined,
    createdAt: formatTime(row.created_at),
    attemptCount: row.attempt_count,
    nextRetryAt: row.next_retry_at ?? null,
    lastError: row.last_error ?? null,
  };
}

function mapDeliveryStats(payload?: DeliveryStatsResponse | null): AlertDeliveryStats {
  if (!payload) {
    return {
      windowHours: 24,
      totalSent: 0,
      totalFailed: 0,
      successRate: 0,
      byIntegration: []
    };
  }

  return {
    windowHours: Number(payload.window_hours || 24),
    totalSent: Number(payload.total_sent || 0),
    totalFailed: Number(payload.total_failed || 0),
    successRate: Number(payload.success_rate || 0),
    byIntegration: Array.isArray(payload.by_integration)
      ? payload.by_integration.map((item) => {
          const sent = Number(item.sent || 0);
          const failed = Number(item.failed || 0);
          const total = sent + failed;
          const successRate = total > 0 ? Number(((sent / total) * 100).toFixed(1)) : 0;
          return {
            integrationId: `int-${item.integration_id}`,
            name: item.name || `integration-${item.integration_id}`,
            type: item.type || "webhook",
            sent,
            failed,
            successRate
          };
        })
      : []
  };
}

export function createAlertsApi() {
  return {
    async getAlerts(token: string, options?: { signal?: AbortSignal }): Promise<AlertRecord[]> {
      // 后端 /alerts 返回 paginated envelope（含 total/page/page_size），
      // core.ts 的 request() 会把带 total 字段的响应整体透传，所以这里需要 unwrapPaginated 拆出 items。
      const payload = await request<PaginatedEnvelope<AlertResponse[]>>("/alerts", { token, signal: options?.signal });
      const { items } = unwrapPaginated(payload);
      return items.map((row) => mapAlert(row));
    },

    async getAlertsPaginated(
      token: string,
      options?: {
        page?: number;
        pageSize?: number;
        sortBy?: "triggered_at" | "severity" | "status" | "node_name";
        sortOrder?: "asc" | "desc";
        status?: string;
        severity?: string;
        keyword?: string;
        signal?: AbortSignal;
      },
    ): Promise<{ items: AlertRecord[]; total: number; page: number; pageSize: number }> {
      const query = new URLSearchParams();
      if (options?.page) query.set("page", String(options.page));
      if (options?.pageSize) query.set("page_size", String(options.pageSize));
      if (options?.sortBy) query.set("sort_by", options.sortBy);
      if (options?.sortOrder) query.set("sort_order", options.sortOrder);
      if (options?.status) query.set("status", options.status);
      if (options?.severity) query.set("severity", options.severity);
      if (options?.keyword) query.set("keyword", options.keyword);
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<PaginatedEnvelope<AlertResponse[]>>(`/alerts${suffix}`, {
        token,
        signal: options?.signal,
      });
      const result = unwrapPaginated(payload);
      return { items: result.items.map(mapAlert), total: result.total, page: result.page, pageSize: result.pageSize };
    },

    async getAlert(token: string, alertId: string, options?: { signal?: AbortSignal }): Promise<AlertRecord> {
      const numericId = parseNumericId(alertId, "alert");
      const row = await request<AlertResponse>(`/alerts/${numericId}`, { token, signal: options?.signal });
      return mapAlert(row);
    },

    /**
     * Fetch the `group_info` block emitted by GET /alerts/:id — the in-memory
     * grouping window counter. `count > 1` means the in-memory dispatcher has
     * seen ≥2 alerts sharing the same (error_code, node, tags) key within the
     * dedup window, and the UI should render a "+N 条同类" badge so operators
     * know they're looking at one symptom of a cluster, not a single event.
     */
    async getAlertGroupInfo(
      token: string,
      alertId: string,
      options?: { signal?: AbortSignal },
    ): Promise<{ count: number; siblingNodeIds: number[] }> {
      const numericId = parseNumericId(alertId, "alert");
      const row = await request<AlertResponse & { group_info?: { count?: number; sibling_node_ids?: number[] } }>(
        `/alerts/${numericId}`,
        { token, signal: options?.signal },
      );
      const gi = row.group_info ?? {};
      return { count: gi.count ?? 1, siblingNodeIds: gi.sibling_node_ids ?? [] };
    },

    async getAlertDeliveries(token: string, alertId: string): Promise<AlertDeliveryRecord[]> {
      const numericId = parseNumericId(alertId, "alert");
      const rows = (await request<AlertDeliveryResponse[]>(`/alerts/${numericId}/deliveries`, { token })) ?? [];
      return rows.map((row) => mapAlertDelivery(row));
    },

    async getAlertDeliveryStats(token: string, options?: { hours?: number }): Promise<AlertDeliveryStats> {
      const query = new URLSearchParams();
      if (options?.hours && Number.isFinite(options.hours) && options.hours > 0) {
        query.set("hours", String(Math.floor(options.hours)));
      }
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const data = await request<DeliveryStatsResponse>(`/alerts/delivery-stats${suffix}`, { token });
      return mapDeliveryStats(data);
    },

    async ackAlert(token: string, alertId: string): Promise<AlertRecord> {
      const numericId = parseNumericId(alertId, "alert");
      const row = await request<AlertResponse>(`/alerts/${numericId}/ack`, {
        method: "POST",
        token
      });
      return mapAlert(row);
    },

    async resolveAlert(token: string, alertId: string): Promise<AlertRecord> {
      const numericId = parseNumericId(alertId, "alert");
      const row = await request<AlertResponse>(`/alerts/${numericId}/resolve`, {
        method: "POST",
        token
      });
      return mapAlert(row);
    },

    async retryAlertDelivery(token: string, alertId: string, integrationId: string): Promise<AlertDeliveryRetryResult> {
      const numericAlertID = parseNumericId(alertId, "alert");
      const numericIntegrationID = parseNumericId(integrationId, "int");
      const data = await request<RetryAlertDeliveryResponse>(`/alerts/${numericAlertID}/retry-delivery`, {
        method: "POST",
        token,
        body: {
          integration_id: numericIntegrationID
        }
      });
      return {
        ok: Boolean(data?.ok),
        message: data?.message ?? i18n.t("common.resendComplete"),
        delivery: mapAlertDelivery(data.delivery)
      };
    },

    async getAlertUnreadCount(token: string): Promise<{ total: number; critical: number; warning: number }> {
      const data = await request<{ total: number; critical: number; warning: number }>("/alerts/unread-count", { token });
      return {
        total: Number(data?.total ?? 0),
        critical: Number(data?.critical ?? 0),
        warning: Number(data?.warning ?? 0),
      };
    },

    async getRecentAlerts(token: string, options?: { limit?: number; signal?: AbortSignal }): Promise<AlertRecord[]> {
      const query = new URLSearchParams();
      query.set("status", "open");
      if (options?.limit) {
        query.set("limit", String(options.limit));
      }
      // 同 getAlerts：后端返回 paginated envelope。
      const payload = await request<PaginatedEnvelope<AlertResponse[]>>(`/alerts?${query.toString()}`, { token, signal: options?.signal });
      const { items } = unwrapPaginated(payload);
      return items.map((row) => mapAlert(row));
    },

    async retryFailedDeliveries(token: string, alertId: string): Promise<AlertBulkRetryResult> {
      const numericAlertID = parseNumericId(alertId, "alert");
      const data = await request<RetryFailedDeliveriesResponse>(`/alerts/${numericAlertID}/retry-failed-deliveries`, {
        method: "POST",
        token
      });
      return {
        ok: Boolean(data?.ok),
        message: data?.message ?? i18n.t("common.batchResendComplete"),
        totalFailed: Number(data?.total_failed ?? 0),
        successCount: Number(data?.success_count ?? 0),
        failedCount: Number(data?.failed_count ?? 0),
        newDeliveries: Array.isArray(data?.new_deliveries)
          ? data.new_deliveries.map((one) => mapAlertDelivery(one))
          : []
      };
    }
  };
}
