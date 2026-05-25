import { request } from "./core";

export type SettingDef = {
  key: string;
  env_var: string;
  code_default: string;
  type: "int" | "bool" | "duration" | "string";
  category: string;
  description: string;
  min?: string;
  max?: string;
  requires_restart?: boolean;
};

export type ResolvedSetting = {
  value: string;
  source: "db" | "env" | "default";
  updated_at: string | null;
};

export type SettingsResponse = {
  definitions: SettingDef[];
  values: Record<string, ResolvedSetting>;
};

export type SecurityRiskSeverity = "info" | "warning" | "critical";
export type SecurityRiskCode =
  | "root_ssh_users"
  | "reused_ssh_keys"
  | "sudo_enabled_nodes"
  | "broad_scope_ssh_keys"
  | "disabled_ssh_keys_in_use"
  | "expired_ssh_keys_in_use"
  | "stale_ssh_keys"
  | "recent_credential_operations"
  | "privileged_users_without_totp"
  | "audit_log_integrity_posture"
  | "ssh_host_key_trust_posture"
  | "deployment_secret_posture"
  | "backup_restore_posture"
  | "weak_security_defaults";

export type SecurityRiskItem = {
  code: SecurityRiskCode;
  severity: SecurityRiskSeverity;
  title: string;
  description: string;
  count: number;
  examples: string[];
};

export type SecurityRiskSummary = {
  generatedAt: string;
  summary: {
    totalRisks: number;
    categories: number;
  };
  items: SecurityRiskItem[];
};

type SecurityRiskSummaryRaw = {
  generated_at?: string;
  summary?: {
    total_risks?: number | string;
    categories?: number | string;
  };
  items?: SecurityRiskItemRaw[];
};

type SecurityRiskItemRaw = {
  code?: string;
  severity?: string;
  title?: string;
  description?: string;
  count?: number | string;
  examples?: unknown;
};

function finiteNumber(value: unknown, fallback = 0): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function normalizeRiskSeverity(value?: string): SecurityRiskSeverity {
  if (value === "info" || value === "critical") {
    return value;
  }
  return "warning";
}

function normalizeRiskCode(value?: string): SecurityRiskCode {
  switch (value) {
    case "root_ssh_users":
    case "reused_ssh_keys":
    case "sudo_enabled_nodes":
    case "broad_scope_ssh_keys":
    case "disabled_ssh_keys_in_use":
    case "expired_ssh_keys_in_use":
    case "stale_ssh_keys":
    case "recent_credential_operations":
    case "privileged_users_without_totp":
    case "audit_log_integrity_posture":
    case "ssh_host_key_trust_posture":
    case "deployment_secret_posture":
    case "backup_restore_posture":
    case "weak_security_defaults":
      return value;
    default:
      return "weak_security_defaults";
  }
}

function mapSecurityRiskItem(row: SecurityRiskItemRaw): SecurityRiskItem {
  return {
    code: normalizeRiskCode(row.code),
    severity: normalizeRiskSeverity(row.severity),
    title: String(row.title ?? ""),
    description: String(row.description ?? ""),
    count: finiteNumber(row.count),
    examples: Array.isArray(row.examples) ? row.examples.map((item) => String(item)).filter(Boolean) : [],
  };
}

/** @internal exported for mapper tests */
export function mapSecurityRiskSummary(raw: SecurityRiskSummaryRaw | null | undefined): SecurityRiskSummary {
  return {
    generatedAt: String(raw?.generated_at ?? ""),
    summary: {
      totalRisks: finiteNumber(raw?.summary?.total_risks),
      categories: finiteNumber(raw?.summary?.categories),
    },
    items: Array.isArray(raw?.items) ? raw.items.map((item) => mapSecurityRiskItem(item)) : [],
  };
}

export function createSettingsApi() {
  return {
    async getSettings(token: string): Promise<SettingsResponse> {
      return (await request<SettingsResponse>("/settings", { token })) ?? { definitions: [], values: {} };
    },

    async getSecurityRiskSummary(token: string): Promise<SecurityRiskSummary> {
      const raw = await request<SecurityRiskSummaryRaw>("/settings/security-risk-summary", { token });
      return mapSecurityRiskSummary(raw);
    },

    async updateSettings(token: string, settings: Record<string, string>): Promise<void> {
      await request("/settings", {
        method: "PUT",
        token,
        body: settings,
      });
    },

    async resetSetting(token: string, key: string): Promise<void> {
      await request(`/settings/${key}`, {
        method: "DELETE",
        token,
      });
    },
  };
}
