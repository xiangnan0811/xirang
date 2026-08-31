import { request } from "./core";
import { finiteNumber } from "./number-utils";

type SettingDefRaw = {
  key?: string;
  env_var?: string;
  code_default?: string;
  type?: string;
  category?: string;
  description?: string;
  min?: string;
  max?: string;
  requires_restart?: boolean;
  sensitive?: boolean;
};

export type SettingDef = {
  key: string;
  envVar: string;
  codeDefault: string;
  type: "int" | "bool" | "duration" | "string";
  category: string;
  description: string;
  min?: string;
  max?: string;
  requiresRestart?: boolean;
  sensitive?: boolean;
};

type ResolvedSettingRaw = {
  value?: string;
  source?: string;
  updated_at?: string | null;
};

export type ResolvedSetting = {
  value: string;
  source: "db" | "env" | "default";
  updatedAt: string | null;
};

type SettingsResponseRaw = {
  definitions?: SettingDefRaw[];
  values?: Record<string, ResolvedSettingRaw>;
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
  | "admin_recovery_posture"
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
    case "admin_recovery_posture":
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

function mapSettingDef(row: SettingDefRaw): SettingDef {
  const type = row.type === "int" || row.type === "bool" || row.type === "duration" || row.type === "string"
    ? row.type
    : "string";
  return {
    key: String(row.key ?? ""),
    envVar: String(row.env_var ?? ""),
    codeDefault: String(row.code_default ?? ""),
    type,
    category: String(row.category ?? ""),
    description: String(row.description ?? ""),
    min: row.min,
    max: row.max,
    requiresRestart: Boolean(row.requires_restart),
    sensitive: Boolean(row.sensitive),
  };
}

function mapResolvedSetting(row: ResolvedSettingRaw): ResolvedSetting {
  return {
    value: String(row.value ?? ""),
    source: row.source === "db" || row.source === "env" ? row.source : "default",
    updatedAt: row.updated_at ?? null,
  };
}

/** @internal exported for mapper tests */
export function mapSettingsResponse(raw: SettingsResponseRaw | null | undefined): SettingsResponse {
  const values: Record<string, ResolvedSetting> = {};
  if (raw?.values && typeof raw.values === "object") {
    for (const [key, value] of Object.entries(raw.values)) {
      values[key] = mapResolvedSetting(value ?? {});
    }
  }
  return {
    definitions: Array.isArray(raw?.definitions) ? raw.definitions.map(mapSettingDef) : [],
    values,
  };
}

export function createSettingsApi() {
  return {
    async getSettings(token: string): Promise<SettingsResponse> {
      const raw = await request<SettingsResponseRaw>("/settings", { token });
      return mapSettingsResponse(raw);
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
