import { request } from "./core";

export type SettingDef = {
  key: string;
  env_var: string;
  code_default: string;
  type: "int" | "bool" | "duration" | "string";
  category: string;
  description: string;
  min?: string;
  max?: string;
  requires_restart?: boolean;
};

export type ResolvedSetting = {
  value: string;
  source: "db" | "env" | "default";
  updated_at: string | null;
};

export type SettingsResponse = {
  definitions: SettingDef[];
  values: Record<string, ResolvedSetting>;
};

export function createSettingsApi() {
  return {
    async getSettings(token: string): Promise<SettingsResponse> {
      return (await request<SettingsResponse>("/settings", { token })) ?? { definitions: [], values: {} };
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
