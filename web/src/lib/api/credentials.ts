import { request } from "./core";
import { finiteNumber } from "./number-utils";
import type {
  AppCredential,
  AppCredentialInput,
  ConfigField,
  ProfileSchema,
} from "@/types/domain";

export type { AppCredential, AppCredentialInput, ConfigField, ProfileSchema };

/** @deprecated Use AppCredential (single domain type). */
export type AppCredentialResponse = AppCredential;

// ── Wire shapes (API boundary only) ──

type RawAppCredential = {
  id?: unknown;
  name?: unknown;
  type?: unknown;
  description?: unknown;
  config?: unknown;
  has_password?: unknown;
  reference_count?: unknown;
  created_at?: unknown;
  updated_at?: unknown;
};

type RawProfileSchema = {
  id?: unknown;
  name?: unknown;
  description?: unknown;
  credential_type?: unknown;
  is_docker?: unknown;
  config_schema?: unknown;
};

/** Wire body for create/update (backend expects snake_case keys). */
type WireAppCredentialInput = {
  type: string;
  name: string;
  description?: string;
  host?: string;
  port?: string;
  user?: string;
  password?: string;
  container_name?: string;
};

function asString(value: unknown, fallback = ""): string {
  if (typeof value === "string") return value;
  if (value == null) return fallback;
  return String(value);
}

function asID(value: unknown): number {
  const n = finiteNumber(value, 0);
  return n >= 0 ? Math.trunc(n) : 0;
}

function normalizeStringRecord(raw: unknown): Record<string, string> {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof k !== "string" || k === "") continue;
    if (v == null) continue;
    out[k] = typeof v === "string" ? v : String(v);
  }
  return out;
}

function normalizeConfigField(raw: unknown): ConfigField | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const o = raw as Record<string, unknown>;
  const key = asString(o.key).trim();
  if (!key) return null;
  return {
    key,
    label: asString(o.label, key),
    type: asString(o.type, "text"),
    required: Boolean(o.required),
    placeholder: o.placeholder == null ? undefined : asString(o.placeholder),
  };
}

function toWireCredentialInput(input: AppCredentialInput): WireAppCredentialInput {
  return {
    type: input.type,
    name: input.name,
    description: input.description,
    host: input.host,
    port: input.port,
    user: input.user,
    password: input.password,
    container_name: input.containerName,
  };
}

export function mapAppCredential(raw: RawAppCredential | null | undefined): AppCredential {
  const row = raw ?? {};
  return {
    id: asID(row.id),
    name: asString(row.name),
    type: asString(row.type),
    description: asString(row.description),
    config: normalizeStringRecord(row.config),
    hasPassword: Boolean(row.has_password),
    referenceCount: finiteNumber(row.reference_count),
    createdAt: asString(row.created_at),
    updatedAt: asString(row.updated_at),
  };
}

export function mapProfileSchema(raw: RawProfileSchema | null | undefined): ProfileSchema {
  const row = raw ?? {};
  const schemaRaw = row.config_schema;
  const configSchema = Array.isArray(schemaRaw)
    ? schemaRaw.map(normalizeConfigField).filter((f): f is ConfigField => f != null)
    : [];
  return {
    id: asString(row.id),
    name: asString(row.name),
    description: asString(row.description),
    credentialType: asString(row.credential_type),
    isDocker: Boolean(row.is_docker),
    configSchema,
  };
}

// ── API factory ──

export function createCredentialsApi() {
  return {
    async list(
      token: string,
      signal?: AbortSignal,
    ): Promise<AppCredential[]> {
      const rows = await request<RawAppCredential[]>("/app-credentials", {
        token,
        signal,
      });
      return (Array.isArray(rows) ? rows : []).map(mapAppCredential);
    },

    async get(
      token: string,
      id: number,
      signal?: AbortSignal,
    ): Promise<AppCredential> {
      const row = await request<RawAppCredential>(`/app-credentials/${id}`, {
        token,
        signal,
      });
      return mapAppCredential(row);
    },

    async create(
      token: string,
      input: AppCredentialInput,
    ): Promise<AppCredential> {
      const row = await request<RawAppCredential>("/app-credentials", {
        method: "POST",
        token,
        body: toWireCredentialInput(input),
      });
      return mapAppCredential(row);
    },

    async update(
      token: string,
      id: number,
      input: AppCredentialInput,
    ): Promise<AppCredential> {
      const row = await request<RawAppCredential>(`/app-credentials/${id}`, {
        method: "PUT",
        token,
        body: toWireCredentialInput(input),
      });
      return mapAppCredential(row);
    },

    delete(token: string, id: number): Promise<void> {
      return request<void>(`/app-credentials/${id}`, {
        method: "DELETE",
        token,
      });
    },

    async listProfiles(
      token: string,
      signal?: AbortSignal,
    ): Promise<ProfileSchema[]> {
      const rows = await request<RawProfileSchema[]>("/app-credentials/profiles", {
        token,
        signal,
      });
      return (Array.isArray(rows) ? rows : []).map(mapProfileSchema);
    },
  };
}
