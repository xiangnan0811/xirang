import type { AppCredential, ProfileSchema } from "@/types/domain";
import { request } from "./core";

export function createAppCredentialsApi() {
  return {
    async getCredentials(token: string, signal?: AbortSignal): Promise<AppCredential[]> {
      return (await request<AppCredential[]>("/app-credentials", { token, signal })) ?? [];
    },

    async getProfiles(token: string, signal?: AbortSignal): Promise<ProfileSchema[]> {
      return (await request<ProfileSchema[]>("/app-credentials/profiles", { token, signal })) ?? [];
    },
  };
}
