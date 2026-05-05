import { request } from "./core";

// ── Types ──

export interface AppCredentialResponse {
  id: number;
  name: string;
  type: string;
  description: string;
  config: Record<string, string>;
  has_password: boolean;
  reference_count: number;
  created_at: string;
  updated_at: string;
}

export interface AppCredentialInput {
  type: string;
  name: string;
  description?: string;
  host?: string;
  port?: string;
  user?: string;
  password?: string;
  container_name?: string;
}

export interface ProfileSchema {
  id: string;
  name: string;
  description: string;
  credential_type: string;
  is_docker: boolean;
  config_schema: ConfigField[];
}

export interface ConfigField {
  key: string;
  label: string;
  type: string; // "text" | "password" | "number"
  required: boolean;
  placeholder?: string;
}

// ── API factory ──

export function createCredentialsApi() {
  return {
    list(
      token: string,
      signal?: AbortSignal,
    ): Promise<AppCredentialResponse[]> {
      return request<AppCredentialResponse[]>("/app-credentials", {
        token,
        signal,
      });
    },

    get(
      token: string,
      id: number,
      signal?: AbortSignal,
    ): Promise<AppCredentialResponse> {
      return request<AppCredentialResponse>(`/app-credentials/${id}`, {
        token,
        signal,
      });
    },

    create(
      token: string,
      input: AppCredentialInput,
    ): Promise<AppCredentialResponse> {
      return request<AppCredentialResponse>("/app-credentials", {
        method: "POST",
        token,
        body: input,
      });
    },

    update(
      token: string,
      id: number,
      input: AppCredentialInput,
    ): Promise<AppCredentialResponse> {
      return request<AppCredentialResponse>(`/app-credentials/${id}`, {
        method: "PUT",
        token,
        body: input,
      });
    },

    delete(token: string, id: number): Promise<void> {
      return request<void>(`/app-credentials/${id}`, {
        method: "DELETE",
        token,
      });
    },

    listProfiles(
      token: string,
      signal?: AbortSignal,
    ): Promise<ProfileSchema[]> {
      return request<ProfileSchema[]>("/app-credentials/profiles", {
        token,
        signal,
      });
    },
  };
}
