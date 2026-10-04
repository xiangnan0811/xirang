import type { AutomationRule, AutomationRuleInput } from "@/types/domain";
import { request, unwrapPaginated, type PaginatedEnvelope } from "./core";
import { finiteNumber } from "./number-utils";

type RawAutomationRule = {
  id: number;
  name?: string;
  description?: string;
  event_type?: string;
  event_filter?: unknown;
  action_type?: string;
  action_config?: unknown;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
};

const logEvents = ["anomaly_detected", "backup_failed", "backup_succeeded", "drill_failed"] as const;
const logActions = ["pause_policy", "disable_policy", "trigger_task", "send_notification"] as const;

export type AutomationRuleLog = {
  id: number;
  ruleId: number;
  eventType: typeof logEvents[number] | "unknown";
  actionType: typeof logActions[number] | "unknown";
  result: "success" | "error" | "unknown";
  createdAt: string;
  errorCode: "ACTION_FAILED" | null;
  targetTaskId: number | null;
  targetTaskRunId: number | null;
};

export type AutomationRuleLogPage = {
  items: AutomationRuleLog[];
  total: number;
  page: number;
  pageSize: number;
};

export type AutomationRuleLogQuery = {
  ruleId?: number;
  result?: "success" | "error";
  page?: number;
  pageSize?: number;
};

type RawAutomationRuleLog = Partial<Record<
  "id" | "rule_id" | "event_type" | "action_type" | "result" | "created_at" |
  "error_code" | "target_task_id" | "target_task_run_id", unknown
>>;

function safeLogTarget(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : null;
}

function mapLog(row: RawAutomationRuleLog): AutomationRuleLog {
  const actionType = logActions.find((value) => value === row.action_type) ?? "unknown";
  const taskId = safeLogTarget(row.target_task_id);
  const runId = safeLogTarget(row.target_task_run_id);
  const validTargets = actionType === "trigger_task" && taskId !== null && runId !== null;
  return {
    id: finiteNumber(row.id),
    ruleId: finiteNumber(row.rule_id),
    eventType: logEvents.find((value) => value === row.event_type) ?? "unknown",
    actionType,
    result: row.result === "success" || row.result === "error" ? row.result : "unknown",
    createdAt: typeof row.created_at === "string" ? row.created_at : "",
    errorCode: row.result === "error" && row.error_code === "ACTION_FAILED" ? "ACTION_FAILED" : null,
    targetTaskId: validTargets ? taskId : null,
    targetTaskRunId: validTargets ? runId : null,
  };
}

function asStringRecord(value: unknown): Record<string, string> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return {};
  }
  const out: Record<string, string> = {};
  for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
    if (typeof entry === "string") {
      out[key] = entry;
    } else if (typeof entry === "number" || typeof entry === "boolean") {
      out[key] = String(entry);
    }
  }
  return out;
}

function parseJsonRecord(raw: unknown): Record<string, string> {
  if (typeof raw === "string") {
    const trimmed = raw.trim();
    if (!trimmed) {
      return {};
    }
    try {
      return asStringRecord(JSON.parse(trimmed) as unknown);
    } catch {
      return {};
    }
  }
  return asStringRecord(raw);
}

function mapRule(row: RawAutomationRule): AutomationRule {
  return {
    id: row.id,
    name: String(row.name ?? ""),
    description: String(row.description ?? ""),
    eventType: String(row.event_type ?? ""),
    eventFilter: parseJsonRecord(row.event_filter),
    actionType: String(row.action_type ?? ""),
    actionConfig: parseJsonRecord(row.action_config),
    enabled: Boolean(row.enabled),
    createdAt: String(row.created_at ?? ""),
    updatedAt: String(row.updated_at ?? ""),
  };
}

function toRuleWire(input: AutomationRuleInput) {
  return {
    name: input.name,
    description: input.description,
    event_type: input.eventType,
    event_filter: JSON.stringify(input.eventFilter ?? {}),
    action_type: input.actionType,
    action_config: JSON.stringify(input.actionConfig ?? {}),
    enabled: input.enabled,
  };
}

export function createAutomationRulesApi() {
  return {
    async list(token: string, options?: { signal?: AbortSignal }): Promise<AutomationRule[]> {
      const rows = (await request<RawAutomationRule[]>("/automation-rules", { token, signal: options?.signal })) ?? [];
      return rows.map(mapRule);
    },

    async listLogs(
      token: string,
      query: AutomationRuleLogQuery = {},
      options?: { signal?: AbortSignal },
    ): Promise<AutomationRuleLogPage> {
      const params = new URLSearchParams();
      if (query.ruleId !== undefined) params.set("rule_id", String(query.ruleId));
      if (query.result !== undefined) params.set("result", query.result);
      if (query.page !== undefined) params.set("page", String(query.page));
      if (query.pageSize !== undefined) params.set("page_size", String(query.pageSize));
      const response = await request<PaginatedEnvelope<RawAutomationRuleLog[]>>(
        `/automation-rule-logs?${params.toString()}`, { token, signal: options?.signal },
      );
      const page = unwrapPaginated(response);
      return { ...page, items: page.items.map(mapLog) };
    },

    async create(token: string, input: AutomationRuleInput): Promise<AutomationRule> {
      const row = await request<RawAutomationRule>("/automation-rules", {
        method: "POST",
        body: toRuleWire(input),
        token,
      });
      return mapRule(row);
    },

    async update(token: string, id: number, input: AutomationRuleInput): Promise<AutomationRule> {
      const row = await request<RawAutomationRule>(`/automation-rules/${id}`, {
        method: "PUT",
        body: toRuleWire(input),
        token,
      });
      return mapRule(row);
    },

    async delete(token: string, id: number): Promise<void> {
      await request<void>(`/automation-rules/${id}`, { method: "DELETE", token });
    },
  };
}
