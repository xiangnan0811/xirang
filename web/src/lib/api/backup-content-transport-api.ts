import { request } from "./core";

export const BACKUP_CONTENT_TRANSPORT_KEY = "backup_assets.content_allow_insecure_private_network";

const ENVIRONMENT_VARIABLE = "BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK";

export type BackupContentTransportSource = "db" | "env" | "default";

export interface BackupContentTransportSetting {
  enabled: boolean;
  source: BackupContentTransportSource;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function mapSetting(raw: unknown): BackupContentTransportSetting {
  if (!isRecord(raw) || !Array.isArray(raw.definitions) || !isRecord(raw.values)) {
    throw new Error("invalid backup content transport setting");
  }
  const definition = raw.definitions.find((candidate) => (
    isRecord(candidate) && candidate.key === BACKUP_CONTENT_TRANSPORT_KEY
  ));
  const resolved = raw.values[BACKUP_CONTENT_TRANSPORT_KEY];
  if (
    !isRecord(definition) ||
    definition.env_var !== ENVIRONMENT_VARIABLE ||
    definition.code_default !== "false" ||
    definition.type !== "bool" ||
    definition.category !== "backup_assets" ||
    !isRecord(resolved) ||
    (resolved.value !== "true" && resolved.value !== "false") ||
    (resolved.source !== "db" && resolved.source !== "env" && resolved.source !== "default")
  ) {
    throw new Error("invalid backup content transport setting");
  }
  return { enabled: resolved.value === "true", source: resolved.source };
}

export function createBackupContentTransportApi() {
  return {
    async get(token: string, signal?: AbortSignal): Promise<BackupContentTransportSetting> {
      return mapSetting(await request<unknown>("/settings", { token, signal }));
    },

    async update(token: string, enabled: boolean, signal?: AbortSignal): Promise<void> {
      await request("/settings", {
        method: "PUT",
        token,
        signal,
        body: { [BACKUP_CONTENT_TRANSPORT_KEY]: enabled ? "true" : "false" },
      });
    },
  };
}
