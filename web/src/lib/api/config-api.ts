import type {
  ConfigImportEntity,
  ConfigImportResult,
  ConfigImportWarning,
  ConfigImportWarningCode,
} from "@/types/domain";
import { request } from "./core";

const CONFIG_IMPORT_ENTITIES: Record<ConfigImportEntity, true> = {
  nodes: true,
  ssh_keys: true,
  policies: true,
  tasks: true,
  system_settings: true,
};

const CONFIG_IMPORT_WARNING_CODES: Record<ConfigImportWarningCode, true> = {
  invalid_input: true,
  invalid_scope: true,
  unresolved_node_scope: true,
  invalid_private_key: true,
  missing_private_key: true,
  missing_password: true,
  missing_inline_private_key: true,
  unresolved_ssh_key: true,
  duplicate_name: true,
  invalid_reference: true,
  reference_conflict: true,
};

type ConfigImportWire = {
  nodes?: unknown;
  ssh_keys?: unknown;
  policies?: unknown;
  tasks?: unknown;
  system_settings?: unknown;
  imported?: unknown;
  skipped?: unknown;
  created?: unknown;
  updated?: unknown;
  rejected?: unknown;
  disabled_imported?: unknown;
  warnings?: unknown;
  warnings_truncated?: unknown;
};

const MAX_IMPORT_WARNINGS = 100;
const MAX_WARNING_NAME_LENGTH = 120;

export class ConfigImportResultError extends Error {
  readonly code = "CONFIG_IMPORT_RESULT_UNUSABLE" as const;

  constructor() {
    super("config import result is unusable");
    this.name = "ConfigImportResultError";
  }
}

export type ConfigExportPayload = {
  version?: string;
  exported_at?: string;
  data?: Record<string, unknown>;
};

export type ConfigRequestOptions = {
  signal?: AbortSignal;
};

function isConfigImportEntity(value: string): value is ConfigImportEntity {
  return Object.hasOwn(CONFIG_IMPORT_ENTITIES, value);
}

function isConfigImportWarningCode(value: string): value is ConfigImportWarningCode {
  return Object.hasOwn(CONFIG_IMPORT_WARNING_CODES, value);
}

function integerCount(value: unknown): number | null {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0 || value > Number.MAX_SAFE_INTEGER) {
    return null;
  }
  return value;
}

function countOrZero(value: unknown): number {
  return integerCount(value) ?? 0;
}

function warningName(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || trimmed.length > MAX_WARNING_NAME_LENGTH) return undefined;
  for (let index = 0; index < trimmed.length; index += 1) {
    const code = trimmed.charCodeAt(index);
    if (code < 32 || code === 127) return undefined;
  }
  return trimmed;
}

function mapWarning(value: unknown): ConfigImportWarning | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  if (!("entity" in value) || !("code" in value) || !("index" in value)) return null;
  const entity = value.entity;
  const code = value.code;
  const index = integerCount(value.index);
  if (typeof entity !== "string" || !isConfigImportEntity(entity)) return null;
  if (typeof code !== "string" || !isConfigImportWarningCode(code) || index === null) return null;
  const warning: ConfigImportWarning = { entity, index, code };
  if ("name" in value) {
    const name = warningName(value.name);
    if (name) warning.name = name;
  }
  return warning;
}

export function mapConfigImportResult(payload: unknown): ConfigImportResult {
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) {
    throw new ConfigImportResultError();
  }
  // Object check above is the wire boundary; fields stay unknown until counted.
  const data = payload as ConfigImportWire;
  const nodes = countOrZero(data.nodes);
  const sshKeys = countOrZero(data.ssh_keys);
  const policies = countOrZero(data.policies);
  const tasks = countOrZero(data.tasks);
  const systemSettings = countOrZero(data.system_settings);
  const imported = integerCount(data.imported) ?? nodes + sshKeys + policies + tasks + systemSettings;
  const warnings: ConfigImportWarning[] = [];
  let droppedWarnings = 0;
  if (Array.isArray(data.warnings)) {
    for (const entry of data.warnings) {
      const warning = mapWarning(entry);
      if (!warning) continue;
      if (warnings.length < MAX_IMPORT_WARNINGS) {
        warnings.push(warning);
      } else {
        droppedWarnings += 1;
      }
    }
  }
  return {
    nodes,
    sshKeys,
    policies,
    tasks,
    systemSettings,
    imported,
    skipped: countOrZero(data.skipped),
    created: countOrZero(data.created),
    updated: countOrZero(data.updated),
    rejected: countOrZero(data.rejected),
    disabledImported: countOrZero(data.disabled_imported),
    warnings,
    warningsTruncated: countOrZero(data.warnings_truncated) + droppedWarnings,
  };
}

export function createConfigApi() {
  return {
    async exportConfig(
      token: string,
      includeSecrets = false,
      stepUpProof?: string,
      options?: ConfigRequestOptions,
    ): Promise<ConfigExportPayload> {
      const query = includeSecrets ? "?include_secrets=true" : "";
      return request<ConfigExportPayload>(`/config/export${query}`, {
        token,
        stepUpProof,
        signal: options?.signal,
      });
    },

    async importConfig(
      token: string,
      data: Record<string, unknown>,
      conflict: "skip" | "overwrite" = "skip",
      stepUpProof?: string,
      options?: ConfigRequestOptions,
    ): Promise<ConfigImportResult> {
      const query = `?conflict=${conflict}`;
      const payload = await request<unknown>(`/config/import${query}`, {
        method: "POST",
        token,
        stepUpProof,
        body: data,
        signal: options?.signal,
      });
      return mapConfigImportResult(payload);
    },
  };
}
