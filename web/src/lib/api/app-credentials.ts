import type { AppCredential, ProfileSchema } from "./credentials";
import { AuthTransitionRejectedError, getAuthSessionGeneration, isAuthTransitionActive } from "./core";

function assertCurrentRead(generation: number, signal?: AbortSignal): void {
  if (isAuthTransitionActive()) throw new AuthTransitionRejectedError();
  signal?.throwIfAborted();
  if (generation !== getAuthSessionGeneration()) throw new DOMException("Aborted", "AbortError");
}

/**
 * Read-only policy-editor façade. Load the credentials API only when options
 * are requested; a static import would include management code at app startup.
 */
export function createAppCredentialsApi() {
  return {
    async getCredentials(token: string, signal?: AbortSignal): Promise<AppCredential[]> {
      const generation = getAuthSessionGeneration();
      assertCurrentRead(generation, signal);
      const { createCredentialsApi } = await import("./credentials");
      assertCurrentRead(generation, signal);
      return createCredentialsApi().list(token, signal);
    },

    async getProfiles(token: string, signal?: AbortSignal): Promise<ProfileSchema[]> {
      const generation = getAuthSessionGeneration();
      assertCurrentRead(generation, signal);
      const { createCredentialsApi } = await import("./credentials");
      assertCurrentRead(generation, signal);
      return createCredentialsApi().listProfiles(token, signal);
    },
  };
}
