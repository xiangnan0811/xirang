import { request, type Envelope, unwrapData } from "./core";

type ConfigImportBreakdown = {
  nodes?: number;
  ssh_keys?: number;
  policies?: number;
  tasks?: number;
  system_settings?: number;
  imported?: number;
  skipped?: number;
};

export type ConfigExportPayload = {
  version?: string;
  exported_at?: string;
  data?: Record<string, unknown>;
};

function summarizeImportResult(payload: Envelope<ConfigImportBreakdown> | ConfigImportBreakdown): { imported: number; skipped: number } {
  const data = unwrapData(payload) ?? {};
  if (typeof data.imported === "number") {
    return {
      imported: data.imported,
      skipped: typeof data.skipped === "number" ? data.skipped : 0
    };
  }

  return {
    imported: (data.nodes ?? 0) + (data.ssh_keys ?? 0) + (data.policies ?? 0) + (data.tasks ?? 0) + (data.system_settings ?? 0),
    skipped: typeof data.skipped === "number" ? data.skipped : 0
  };
}

export function createConfigApi() {
  return {
    async exportConfig(token: string, includeSecrets = false): Promise<ConfigExportPayload> {
      const query = includeSecrets ? "?include_secrets=true" : "";
      return request<ConfigExportPayload>(`/config/export${query}`, { token });
    },

    async importConfig(token: string, data: Record<string, unknown>, conflict: "skip" | "overwrite" = "skip"): Promise<{ imported: number; skipped: number }> {
      const query = `?conflict=${conflict}`;
      const payload = await request<Envelope<ConfigImportBreakdown>>(`/config/import${query}`, {
        method: "POST",
        token,
        body: data,
      });
      return summarizeImportResult(payload);
    },
  };
}
