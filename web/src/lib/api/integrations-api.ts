import type { IntegrationChannel, IntegrationProbeResult, NewIntegrationInput } from "@/types/domain";
import { parseNumericId, request, type Envelope, unwrapData } from "./core";

type IntegrationResponse = {
  id: number;
  type: IntegrationChannel["type"];
  name: string;
  endpoint: string;
  enabled: boolean;
  fail_threshold: number;
  cooldown_minutes: number;
};

type IntegrationTestResponse = {
  ok: boolean;
  message: string;
  latency_ms?: number;
};

function mapIntegration(row: IntegrationResponse): IntegrationChannel {
  return {
    id: `int-${row.id}`,
    type: row.type,
    name: row.name,
    endpoint: row.endpoint,
    enabled: row.enabled,
    failThreshold: row.fail_threshold,
    cooldownMinutes: row.cooldown_minutes
  };
}

export function createIntegrationsApi() {
  return {
    async getIntegrations(token: string, options?: { signal?: AbortSignal }): Promise<IntegrationChannel[]> {
      const payload = await request<Envelope<IntegrationResponse[]>>("/integrations", { token, signal: options?.signal });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row) => mapIntegration(row));
    },

    async createIntegration(token: string, input: NewIntegrationInput): Promise<IntegrationChannel> {
      const payload = await request<Envelope<IntegrationResponse>>("/integrations", {
        method: "POST",
        token,
        body: {
          type: input.type,
          name: input.name,
          endpoint: input.endpoint,
          enabled: input.enabled,
          fail_threshold: input.failThreshold,
          cooldown_minutes: input.cooldownMinutes
        }
      });
      return mapIntegration(unwrapData(payload));
    },

    async updateIntegration(
      token: string,
      integrationId: string,
      patch: Partial<IntegrationChannel>
    ): Promise<IntegrationChannel> {
      const numericId = parseNumericId(integrationId, "int");
      const payload = await request<Envelope<IntegrationResponse>>(`/integrations/${numericId}`, {
        method: "PUT",
        token,
        body: {
          type: patch.type,
          name: patch.name,
          endpoint: patch.endpoint,
          enabled: patch.enabled,
          fail_threshold: patch.failThreshold,
          cooldown_minutes: patch.cooldownMinutes
        }
      });
      return mapIntegration(unwrapData(payload));
    },

    async testIntegration(token: string, integrationId: string): Promise<IntegrationProbeResult> {
      const numericId = parseNumericId(integrationId, "int");
      const payload = await request<Envelope<IntegrationTestResponse>>(`/integrations/${numericId}/test`, {
        method: "POST",
        token
      });
      const data = unwrapData(payload);
      return {
        ok: Boolean(data?.ok),
        message: data?.message ?? "测试完成",
        latencyMs: Number(data?.latency_ms ?? 0)
      };
    },

    async deleteIntegration(token: string, integrationId: string): Promise<void> {
      const numericId = parseNumericId(integrationId, "int");
      await request(`/integrations/${numericId}`, {
        method: "DELETE",
        token
      });
    }
  };
}
