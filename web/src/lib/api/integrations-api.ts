import type { IntegrationChannel, IntegrationProbeResult, NewIntegrationInput } from "@/types/domain";
import i18n from "@/i18n";
import { parseNumericId, request, type Envelope, unwrapData } from "./core";

type IntegrationResponse = {
  id: number;
  type: IntegrationChannel["type"];
  name: string;
  endpoint: string;
  has_secret: boolean;
  enabled: boolean;
  fail_threshold: number;
  cooldown_minutes: number;
  proxy_url?: string;
};

type IntegrationHintResponse = {
  hint: string;
  created: false;
};

type IntegrationTestResponse = {
  ok: boolean;
  message: string;
  latency_ms?: number;
};

/** 服务端返回域名建议提示时抛出，前端可捕获后弹出确认框 */
export class EndpointHintWarning extends Error {
  hint: string;
  constructor(hint: string) {
    super(hint);
    this.name = "EndpointHintWarning";
    this.hint = hint;
  }
}

function mapIntegration(row: IntegrationResponse): IntegrationChannel {
  return {
    id: `int-${row.id}`,
    type: row.type,
    name: row.name,
    endpoint: row.endpoint,
    hasSecret: Boolean(row.has_secret),
    enabled: row.enabled,
    failThreshold: row.fail_threshold,
    cooldownMinutes: row.cooldown_minutes,
    proxyUrl: row.proxy_url ?? "",
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
      const raw = await request<Envelope<IntegrationResponse> | IntegrationHintResponse>("/integrations", {
        method: "POST",
        token,
        body: {
          type: input.type,
          name: input.name,
          endpoint: input.endpoint || undefined,
          enabled: input.enabled,
          fail_threshold: input.failThreshold,
          cooldown_minutes: input.cooldownMinutes,
          secret: input.secret || undefined,
          skip_endpoint_hint: input.skipEndpointHint ?? false,
          bot_token: input.botToken || undefined,
          chat_id: input.chatId || undefined,
          access_token: input.accessToken || undefined,
          hook_id: input.hookId || undefined,
          webhook_key: input.webhookKey || undefined,
          proxy_url: input.proxyUrl || undefined,
        }
      });
      // 域名建议提示（200 + created:false）
      if (raw && typeof raw === "object" && "created" in raw && raw.created === false) {
        throw new EndpointHintWarning((raw as IntegrationHintResponse).hint ?? "");
      }
      return mapIntegration(unwrapData(raw as Envelope<IntegrationResponse>));
    },

    async updateIntegration(
      token: string,
      integrationId: string,
      patch: Partial<IntegrationChannel> & { secret?: string; skipEndpointHint?: boolean }
    ): Promise<IntegrationChannel> {
      const numericId = parseNumericId(integrationId, "int");
      const raw = await request<Envelope<IntegrationResponse> | IntegrationHintResponse>(`/integrations/${numericId}`, {
        method: "PUT",
        token,
        body: {
          type: patch.type,
          name: patch.name,
          endpoint: patch.endpoint,
          enabled: patch.enabled,
          fail_threshold: patch.failThreshold,
          cooldown_minutes: patch.cooldownMinutes,
          secret: patch.secret || undefined,
          skip_endpoint_hint: patch.skipEndpointHint ?? false,
          proxy_url: patch.proxyUrl ?? undefined,
        }
      });
      // 域名建议提示（200 + updated:false）
      if (raw && typeof raw === "object" && "updated" in raw && (raw as Record<string, unknown>).updated === false) {
        throw new EndpointHintWarning((raw as IntegrationHintResponse).hint ?? "");
      }
      return mapIntegration(unwrapData(raw as Envelope<IntegrationResponse>));
    },

    async patchIntegration(
      token: string,
      integrationId: string,
      patch: Record<string, unknown>
    ): Promise<IntegrationChannel> {
      const numericId = parseNumericId(integrationId, "int");
      const raw = await request<Envelope<IntegrationResponse> | IntegrationHintResponse>(`/integrations/${numericId}`, {
        method: "PATCH",
        token,
        body: patch
      });
      // 域名建议提示（200 + updated:false）
      if (raw && typeof raw === "object" && "updated" in raw && (raw as Record<string, unknown>).updated === false) {
        throw new EndpointHintWarning((raw as IntegrationHintResponse).hint ?? "");
      }
      return mapIntegration(unwrapData(raw as Envelope<IntegrationResponse>));
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
        message: data?.message ?? i18n.t("common.testComplete"),
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
