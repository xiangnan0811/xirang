import type { ConfigImportConflict, ConfigImportPreview, ConfigImportResult } from "@/types/domain";
import { request } from "./core";

// The startup-reachable API client must not eagerly load admin-only response
// validators. Load them after requests, preserving request/auth/error timing.

export type ConfigExportPayload = {
  version?: string;
  exported_at?: string;
  data?: Record<string, unknown>;
};

export type ConfigRequestOptions = {
  signal?: AbortSignal;
};

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
      const { mapConfigImportResult } = await import("./config-import-response");
      return mapConfigImportResult(payload);
    },

    async previewConfig(
      token: string,
      data: Record<string, unknown>,
      conflict: ConfigImportConflict = "skip",
      options?: ConfigRequestOptions,
    ): Promise<ConfigImportPreview> {
      const payload = await request<unknown>(`/config/import/preview?conflict=${conflict}`, {
        method: "POST",
        token,
        body: data,
        signal: options?.signal,
      });
      const { mapConfigImportPreview } = await import("./config-import-response");
      return mapConfigImportPreview(payload, conflict);
    },
  };
}
