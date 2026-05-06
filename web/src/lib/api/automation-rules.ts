import type { AutomationRule, AutomationRuleInput } from "@/types/domain";
import { request } from "./core";

export function createAutomationRulesApi() {
  return {
    async list(token: string, signal?: AbortSignal): Promise<AutomationRule[]> {
      return (await request<AutomationRule[]>("/automation-rules", { token, signal })) ?? [];
    },

    async create(token: string, input: AutomationRuleInput): Promise<AutomationRule> {
      return await request<AutomationRule>("/automation-rules", {
        method: "POST",
        body: input,
        token,
      });
    },

    async update(token: string, id: number, input: AutomationRuleInput): Promise<AutomationRule> {
      return await request<AutomationRule>(`/automation-rules/${id}`, {
        method: "PUT",
        body: input,
        token,
      });
    },

    async delete(token: string, id: number): Promise<void> {
      await request<void>(`/automation-rules/${id}`, { method: "DELETE", token });
    },
  };
}
