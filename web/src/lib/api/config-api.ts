import { request, type Envelope, unwrapData } from "./core";

export function createConfigApi() {
  return {
    async exportConfig(token: string, includeSecrets = false): Promise<Record<string, unknown>> {
      const query = includeSecrets ? "?include_secrets=true" : "";
      const payload = await request<Envelope<Record<string, unknown>>>(`/config/export${query}`, { token });
      return unwrapData(payload) ?? {};
    },

    async importConfig(token: string, data: Record<string, unknown>, conflict: "skip" | "overwrite" = "skip"): Promise<{ imported: number; skipped: number }> {
      const query = `?conflict=${conflict}`;
      const payload = await request<Envelope<{ imported: number; skipped: number }>>(`/config/import${query}`, {
        method: "POST",
        token,
        body: data,
      });
      return unwrapData(payload) ?? { imported: 0, skipped: 0 };
    },
  };
}
