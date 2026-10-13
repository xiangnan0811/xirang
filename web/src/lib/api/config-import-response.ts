import type {
  ConfigImportConflict,
  ConfigImportEntity,
  ConfigImportPendingCheck,
  ConfigImportPendingCheckCode,
  ConfigImportPreview,
  ConfigImportPreviewAsset,
  ConfigImportPreviewAssetEntity,
  ConfigImportPreviewAssetStatus,
  ConfigImportPreviewEntityCounts,
  ConfigImportPreviewSecret,
  ConfigImportPreviewSecretAction,
  ConfigImportPreviewSecretEntity,
  ConfigImportPreviewSecretField,
  ConfigImportPreviewSystemSettings,
  ConfigImportResult,
  ConfigImportWarning,
  ConfigImportWarningCode,
} from "@/types/domain";

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
const MAX_PREVIEW_ITEMS = 100;

const CONFIG_IMPORT_PREVIEW_ENTITY_ORDER = [
  "nodes",
  "ssh_keys",
  "policies",
  "tasks",
  "system_settings",
] as const satisfies readonly ConfigImportEntity[];

const CONFIG_IMPORT_PREVIEW_ASSET_ORDER = [
  "backup_repositories",
  "task_repository_links",
  "backup_retention_policies",
  "recovery_point_holds",
] as const satisfies readonly ConfigImportPreviewAssetEntity[];

const CONFIG_IMPORT_PENDING_CHECK_CODES: Record<ConfigImportPendingCheckCode, true> = {
  retained_secret: true,
  allocated_identity: true,
  pending_reference: true,
};

const CONFIG_IMPORT_PREVIEW_SECRET_ENTITIES: Record<ConfigImportPreviewSecretEntity, true> = {
  nodes: true,
  ssh_keys: true,
};

const CONFIG_IMPORT_PREVIEW_SECRET_FIELDS: Record<ConfigImportPreviewSecretField, true> = {
  password: true,
  private_key: true,
};

const CONFIG_IMPORT_PREVIEW_SECRET_ACTIONS: Record<ConfigImportPreviewSecretAction, true> = {
  supplied: true,
  preserved: true,
  missing: true,
  skipped: true,
  rejected: true,
};

const CONFIG_IMPORT_PREVIEW_ASSET_STATUSES: Record<ConfigImportPreviewAssetStatus, true> = {
  unknown: true,
  not_present: true,
  unsupported: true,
};

const CONFIG_IMPORT_PREVIEW_COUNT_FIELDS = [
  "input",
  "created",
  "updated",
  "skipped",
  "rejected",
  "pending_validation",
  "disabled_imported",
] as const;

export class ConfigImportResultError extends Error {
  readonly code = "CONFIG_IMPORT_RESULT_UNUSABLE" as const;

  constructor() {
    super("config import result is unusable");
    this.name = "ConfigImportResultError";
  }
}

export class ConfigImportPreviewError extends Error {
  readonly code = "CONFIG_IMPORT_PREVIEW_UNUSABLE" as const;

  constructor() {
    super("config import preview is unusable");
    this.name = "ConfigImportPreviewError";
  }
}

function isConfigImportEntity(value: string): value is ConfigImportEntity {
  return Object.hasOwn(CONFIG_IMPORT_ENTITIES, value);
}

function isConfigImportWarningCode(value: string): value is ConfigImportWarningCode {
  return Object.hasOwn(CONFIG_IMPORT_WARNING_CODES, value);
}

function allowlisted<T extends string>(allowlist: Record<T, true>, value: unknown): value is T {
  return typeof value === "string" && Object.hasOwn(allowlist, value);
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
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

function mapPreviewEntityCounts(
  value: unknown,
  entity: ConfigImportEntity,
): ConfigImportPreviewEntityCounts | null {
  if (!isPlainRecord(value) || value.entity !== entity) return null;
  const counts: Record<(typeof CONFIG_IMPORT_PREVIEW_COUNT_FIELDS)[number], number> = {
    input: 0,
    created: 0,
    updated: 0,
    skipped: 0,
    rejected: 0,
    pending_validation: 0,
    disabled_imported: 0,
  };
  for (const field of CONFIG_IMPORT_PREVIEW_COUNT_FIELDS) {
    const count = integerCount(value[field]);
    if (count === null) return null;
    counts[field] = count;
  }
  return {
    entity,
    input: counts.input,
    created: counts.created,
    updated: counts.updated,
    skipped: counts.skipped,
    rejected: counts.rejected,
    pendingValidation: counts.pending_validation,
    disabledImported: counts.disabled_imported,
  };
}

function mapPreviewSystemSettings(value: unknown): ConfigImportPreviewSystemSettings | null {
  if (!isPlainRecord(value)) return null;
  const created = integerCount(value.created);
  const updated = integerCount(value.updated);
  if (
    typeof value.will_write !== "boolean" ||
    typeof value.applies_in_skip !== "boolean" ||
    created === null ||
    updated === null
  ) {
    return null;
  }
  return {
    willWrite: value.will_write,
    created,
    updated,
    appliesInSkip: value.applies_in_skip,
  };
}

function mapPreviewPendingCheck(value: unknown): ConfigImportPendingCheck | null {
  if (!isPlainRecord(value)) return null;
  const entity = value.entity;
  const code = value.code;
  const index = integerCount(value.index);
  if (!allowlisted(CONFIG_IMPORT_ENTITIES, entity)) return null;
  if (!allowlisted(CONFIG_IMPORT_PENDING_CHECK_CODES, code) || index === null) return null;
  return { entity, index, code };
}

function mapPreviewSecret(value: unknown): ConfigImportPreviewSecret | null {
  if (!isPlainRecord(value)) return null;
  const entity = value.entity;
  const field = value.field;
  const action = value.action;
  const index = integerCount(value.index);
  if (!allowlisted(CONFIG_IMPORT_PREVIEW_SECRET_ENTITIES, entity)) return null;
  if (!allowlisted(CONFIG_IMPORT_PREVIEW_SECRET_FIELDS, field)) return null;
  if (!allowlisted(CONFIG_IMPORT_PREVIEW_SECRET_ACTIONS, action) || index === null) return null;
  if (typeof value.present !== "boolean") return null;
  if (entity === "ssh_keys" && field !== "private_key") return null;
  return { entity, index, field, present: value.present, action };
}

function mapPreviewAsset(value: unknown, entity: ConfigImportPreviewAssetEntity): ConfigImportPreviewAsset | null {
  if (!isPlainRecord(value) || value.entity !== entity) return null;
  const status = value.status;
  const input = integerCount(value.input);
  if (!allowlisted(CONFIG_IMPORT_PREVIEW_ASSET_STATUSES, status) || input === null) return null;
  if (input === 0 && status !== "not_present") return null;
  if (input > 0 && status === "not_present") return null;
  return { entity, input, status };
}

function mapAllowlistedWarnings(value: unknown): { items: ConfigImportWarning[]; dropped: number } | null {
  if (!Array.isArray(value)) return null;
  const items: ConfigImportWarning[] = [];
  let dropped = 0;
  for (const entry of value) {
    const warning = mapWarning(entry);
    if (!warning) continue;
    if (items.length < MAX_IMPORT_WARNINGS) {
      items.push(warning);
    } else {
      dropped += 1;
    }
  }
  return { items, dropped };
}

function mapBoundedList<T>(
  value: unknown,
  mapItem: (entry: unknown) => T | null,
  limit: number,
): { items: T[]; dropped: number } | null {
  if (!Array.isArray(value)) return null;
  const items: T[] = [];
  let dropped = 0;
  for (const entry of value) {
    const item = mapItem(entry);
    if (!item) return null;
    if (items.length < limit) {
      items.push(item);
    } else {
      dropped += 1;
    }
  }
  return { items, dropped };
}

export function mapConfigImportPreview(payload: unknown, requestedConflict: ConfigImportConflict): ConfigImportPreview {
  if (!isPlainRecord(payload)) {
    throw new ConfigImportPreviewError();
  }
  if (payload.conflict !== requestedConflict || payload.basis !== "current_snapshot" || payload.requires_apply_validation !== true) {
    throw new ConfigImportPreviewError();
  }
  if (!Array.isArray(payload.entities) || payload.entities.length !== CONFIG_IMPORT_PREVIEW_ENTITY_ORDER.length) {
    throw new ConfigImportPreviewError();
  }
  const entities = CONFIG_IMPORT_PREVIEW_ENTITY_ORDER.map((entity, index) =>
    mapPreviewEntityCounts((payload.entities as unknown[])[index], entity),
  );
  if (entities.some((entity) => entity === null)) {
    throw new ConfigImportPreviewError();
  }
  const systemSettings = mapPreviewSystemSettings(payload.system_settings);
  if (!systemSettings) {
    throw new ConfigImportPreviewError();
  }
  const settingsCounts = entities[CONFIG_IMPORT_PREVIEW_ENTITY_ORDER.indexOf("system_settings")];
  if (
    !settingsCounts ||
    systemSettings.created !== settingsCounts.created ||
    systemSettings.updated !== settingsCounts.updated ||
    systemSettings.willWrite !== settingsCounts.input > 0 ||
    !systemSettings.appliesInSkip
  ) {
    throw new ConfigImportPreviewError();
  }
  const warnings = mapAllowlistedWarnings(payload.warnings);
  const pendingChecks = mapBoundedList(payload.pending_checks, mapPreviewPendingCheck, MAX_PREVIEW_ITEMS);
  const secrets = mapBoundedList(payload.secrets, mapPreviewSecret, MAX_PREVIEW_ITEMS);
  if (!warnings || !pendingChecks || !secrets) {
    throw new ConfigImportPreviewError();
  }
  const warningsTruncated = integerCount(payload.warnings_truncated);
  const pendingChecksTruncated = integerCount(payload.pending_checks_truncated);
  const secretsTruncated = integerCount(payload.secrets_truncated);
  if (
    warningsTruncated === null ||
    pendingChecksTruncated === null ||
    secretsTruncated === null ||
    warningsTruncated > Number.MAX_SAFE_INTEGER - warnings.dropped ||
    pendingChecksTruncated > Number.MAX_SAFE_INTEGER - pendingChecks.dropped ||
    secretsTruncated > Number.MAX_SAFE_INTEGER - secrets.dropped
  ) {
    throw new ConfigImportPreviewError();
  }
  if (!Array.isArray(payload.asset_graph) || payload.asset_graph.length !== CONFIG_IMPORT_PREVIEW_ASSET_ORDER.length) {
    throw new ConfigImportPreviewError();
  }
  const assetGraph = CONFIG_IMPORT_PREVIEW_ASSET_ORDER.map((entity, index) =>
    mapPreviewAsset((payload.asset_graph as unknown[])[index], entity),
  );
  if (assetGraph.some((asset) => asset === null)) {
    throw new ConfigImportPreviewError();
  }
  return {
    conflict: requestedConflict,
    basis: "current_snapshot",
    entities: entities as ConfigImportPreview["entities"],
    warnings: warnings.items,
    pendingChecks: pendingChecks.items,
    pendingChecksTruncated: pendingChecksTruncated + pendingChecks.dropped,
    warningsTruncated: warningsTruncated + warnings.dropped,
    systemSettings,
    secrets: secrets.items,
    secretsTruncated: secretsTruncated + secrets.dropped,
    assetGraph: assetGraph as ConfigImportPreview["assetGraph"],
    requiresApplyValidation: true,
  };
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
