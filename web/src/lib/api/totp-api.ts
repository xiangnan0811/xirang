import { ApiError, request } from "./core";
import { finiteNumber } from "./number-utils";
import type { StepUpAction } from "@/lib/step-up-storage";

export { ALL_STEP_UP_ACTIONS, STEP_UP_ACTIONS } from "@/lib/step-up-storage";
export type { StepUpAction } from "@/lib/step-up-storage";

export interface TOTPSetupResponse {
  secret: string;
  qrUrl: string;
  issuer: string;
  enrollmentId: string;
  expiresAt: string;
}

export interface TOTPVerifyResponse {
  token: string;
  user: {
    id: number;
    username: string;
    role: "admin" | "operator" | "viewer";
    totpEnabled: true;
  };
  recoveryCodes: string[];
}

export interface TOTPLoginResponse {
  token: string;
  user: {
    id: number;
    username: string;
    role: "admin" | "operator" | "viewer";
    totpEnabled: boolean;
  };
}

export interface StepUpProofResponse {
  proof: string;
  expiresAt: string;
  proofTtlSeconds: number;
}

type RawTOTPSetupResponse = {
  secret?: unknown;
  qr_url?: unknown;
  issuer?: unknown;
  enrollment_id?: unknown;
  expires_at?: unknown;
};

export const TOTP_NO_COMMIT_ERROR_CODES = [
  "TOTP_CODE_INVALID",
  "TOTP_ENROLLMENT_REQUIRED",
  "TOTP_ENROLLMENT_EXPIRED",
  "TOTP_ENROLLMENT_CONFLICT",
] as const;

export type TOTPNoCommitErrorCode = (typeof TOTP_NO_COMMIT_ERROR_CODES)[number];

export class TOTPVerifyContractError extends Error {
  readonly code = "TOTP_VERIFY_CONTRACT" as const;

  constructor() {
    super("TOTP activation response did not match the required contract");
    this.name = "TOTPVerifyContractError";
  }
}

type RawTOTPLoginResponse = {
  token?: unknown;
  user?: {
    id?: unknown;
    username?: unknown;
    role?: unknown;
    totp_enabled?: unknown;
  };
};

type RawStepUpProofResponse = {
  proof?: unknown;
  expires_at?: unknown;
  proof_ttl_seconds?: unknown;
};

function mapRole(raw: unknown): "admin" | "operator" | "viewer" {
  return raw === "admin" || raw === "operator" || raw === "viewer" ? raw : "viewer";
}

export function mapTOTPSetupResponse(raw: RawTOTPSetupResponse | null | undefined): TOTPSetupResponse {
  return {
    secret: String(raw?.secret ?? ""),
    qrUrl: String(raw?.qr_url ?? ""),
    issuer: String(raw?.issuer ?? ""),
    enrollmentId: String(raw?.enrollment_id ?? "").trim(),
    expiresAt: String(raw?.expires_at ?? "").trim(),
  };
}

function isAuthRole(value: unknown): value is "admin" | "operator" | "viewer" {
  return value === "admin" || value === "operator" || value === "viewer";
}

function isRecoveryCodeList(value: unknown): value is string[] {
  return Array.isArray(value)
    && value.length > 0
    && value.every((code) => typeof code === "string" && code.length > 0);
}

export function mapTOTPVerifyResponse(raw: unknown): TOTPVerifyResponse {
  if (!raw || typeof raw !== "object") {
    throw new TOTPVerifyContractError();
  }
  if (!("token" in raw) || !("user" in raw) || !("recovery_codes" in raw)) {
    throw new TOTPVerifyContractError();
  }
  const token = raw.token;
  const user = raw.user;
  const recoveryCodes = raw.recovery_codes;
  if (typeof token !== "string" || token.trim() === "" || !user || typeof user !== "object") {
    throw new TOTPVerifyContractError();
  }
  if (!("id" in user) || !("username" in user) || !("role" in user) || !("totp_enabled" in user)) {
    throw new TOTPVerifyContractError();
  }
  const id = user.id;
  const username = user.username;
  const role = user.role;
  if (typeof id !== "number" || !Number.isInteger(id) || id <= 0) {
    throw new TOTPVerifyContractError();
  }
  if (typeof username !== "string" || username.trim() === "" || !isAuthRole(role) || user.totp_enabled !== true) {
    throw new TOTPVerifyContractError();
  }
  if (!isRecoveryCodeList(recoveryCodes)) {
    throw new TOTPVerifyContractError();
  }
  return {
    token,
    user: {
      id,
      username,
      role,
      totpEnabled: true,
    },
    recoveryCodes,
  };
}

export function totpVerifyErrorCode(error: unknown): string | null {
  if (!(error instanceof ApiError)) {
    return null;
  }
  const detail = error.detail;
  if (!detail || typeof detail !== "object" || !("data" in detail)) {
    return null;
  }
  const data = detail.data;
  if (!data || typeof data !== "object" || !("error_code" in data)) {
    return null;
  }
  const code = data.error_code;
  return typeof code === "string" && code.length > 0 ? code : null;
}

export function classifyTOTPVerifyFailure(error: unknown): "no_commit" | "ambiguous" {
  if (!(error instanceof ApiError) || error.status >= 500) {
    return "ambiguous";
  }
  const code = totpVerifyErrorCode(error);
  if (code !== null && (TOTP_NO_COMMIT_ERROR_CODES as readonly string[]).includes(code)) {
    return "no_commit";
  }
  return "ambiguous";
}

export function mapTOTPLoginResponse(raw: RawTOTPLoginResponse | null | undefined): TOTPLoginResponse {
  return {
    token: String(raw?.token ?? ""),
    user: {
      id: finiteNumber(raw?.user?.id),
      username: String(raw?.user?.username ?? ""),
      role: mapRole(raw?.user?.role),
      totpEnabled: Boolean(raw?.user?.totp_enabled),
    },
  };
}

export function mapStepUpProofResponse(raw: RawStepUpProofResponse | null | undefined): StepUpProofResponse {
  return {
    proof: String(raw?.proof ?? ""),
    expiresAt: String(raw?.expires_at ?? ""),
    proofTtlSeconds: finiteNumber(raw?.proof_ttl_seconds),
  };
}

export function createTOTPApi() {
  return {
    async totpSetup(token: string): Promise<TOTPSetupResponse> {
      const raw = await request<RawTOTPSetupResponse>("/auth/2fa/setup", {
        method: "POST",
        token,
      });
      return mapTOTPSetupResponse(raw);
    },

    async totpVerify(token: string, code: string, enrollmentId: string, authTransitionId: number): Promise<TOTPVerifyResponse> {
      const enrollment = enrollmentId.trim();
      if (!enrollment) {
        throw new Error("enrollment_id is required");
      }
      const raw = await request<unknown>("/auth/2fa/verify", {
        method: "POST",
        token,
        authTransitionId,
        body: { code, enrollment_id: enrollment },
      });
      return mapTOTPVerifyResponse(raw);
    },

    async totpDisable(token: string, password: string, totpCode: string): Promise<void> {
      await request("/auth/2fa/disable", {
        method: "POST",
        token,
        body: { password, totp_code: totpCode },
      });
    },

    async totpLogin(loginToken: string, totpCode: string): Promise<TOTPLoginResponse> {
      const raw = await request<RawTOTPLoginResponse>("/auth/2fa/login", {
        method: "POST",
        body: { login_token: loginToken, totp_code: totpCode },
      });
      return mapTOTPLoginResponse(raw);
    },

    async requestStepUpProof(token: string, code: string, action: StepUpAction): Promise<StepUpProofResponse> {
      const raw = await request<RawStepUpProofResponse>("/auth/step-up", {
        method: "POST",
        token,
        body: { code, step_up_action: action },
      });
      return mapStepUpProofResponse(raw);
    },
  };
}
