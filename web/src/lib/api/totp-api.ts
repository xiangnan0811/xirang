import { request } from "./core";
import { finiteNumber } from "./number-utils";
import type { StepUpAction } from "@/lib/step-up-storage";

export { ALL_STEP_UP_ACTIONS, STEP_UP_ACTIONS } from "@/lib/step-up-storage";
export type { StepUpAction } from "@/lib/step-up-storage";

export interface TOTPSetupResponse {
  secret: string;
  qrUrl: string;
  issuer: string;
}

export interface TOTPVerifyResponse {
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
};

type RawTOTPVerifyResponse = {
  recovery_codes?: unknown;
};

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
  };
}

export function mapTOTPVerifyResponse(raw: RawTOTPVerifyResponse | null | undefined): TOTPVerifyResponse {
  return {
    recoveryCodes: Array.isArray(raw?.recovery_codes)
      ? raw.recovery_codes.map((code) => String(code))
      : [],
  };
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

    async totpVerify(token: string, code: string): Promise<TOTPVerifyResponse> {
      const raw = await request<RawTOTPVerifyResponse>("/auth/2fa/verify", {
        method: "POST",
        token,
        body: { code },
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
