import { createCredentialsApi, type AppCredential, type ProfileSchema } from "./credentials";

/**
 * Compatibility façade used by policy-editor and other call sites.
 * Always maps wire snake_case → camelCase domain types (via credentials API).
 */
export function createAppCredentialsApi() {
  const api = createCredentialsApi();
  return {
    async getCredentials(token: string, signal?: AbortSignal): Promise<AppCredential[]> {
      return api.list(token, signal);
    },

    async getProfiles(token: string, signal?: AbortSignal): Promise<ProfileSchema[]> {
      return api.listProfiles(token, signal);
    },
  };
}
