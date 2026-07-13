import { request } from "./core";
import type { StepUpAction } from "@/lib/step-up-storage";

export { ALL_STEP_UP_ACTIONS, STEP_UP_ACTIONS } from "@/lib/step-up-storage";
export type { StepUpAction } from "@/lib/step-up-storage";

export interface TOTPSetupResponse {
  secret: string;
  qr_url: string;
  issuer: string;
}

export interface TOTPVerifyResponse {
  recovery_codes: string[];
}

export interface TOTPLoginResponse {
  token: string;
  user: {
    id: number;
    username: string;
    role: string;
    totp_enabled: boolean;
  };
}

export interface StepUpProofResponse {
  proof: string;
  expires_at: string;
  proof_ttl_seconds: number;
}

export function createTOTPApi() {
  return {
    async totpSetup(token: string): Promise<TOTPSetupResponse> {
      return request<TOTPSetupResponse>("/auth/2fa/setup", {
        method: "POST",
        token,
      });
    },

    async totpVerify(token: string, code: string): Promise<TOTPVerifyResponse> {
      return request<TOTPVerifyResponse>("/auth/2fa/verify", {
        method: "POST",
        token,
        body: { code },
      });
    },

    async totpDisable(token: string, password: string, totpCode: string): Promise<void> {
      await request("/auth/2fa/disable", {
        method: "POST",
        token,
        body: { password, totp_code: totpCode },
      });
    },

    async totpLogin(loginToken: string, totpCode: string): Promise<TOTPLoginResponse> {
      return request<TOTPLoginResponse>("/auth/2fa/login", {
        method: "POST",
        body: { login_token: loginToken, totp_code: totpCode },
      });
    },

    async requestStepUpProof(token: string, code: string, action: StepUpAction): Promise<StepUpProofResponse> {
      return request<StepUpProofResponse>("/auth/step-up", {
        method: "POST",
        token,
        body: { code, step_up_action: action },
      });
    },
  };
}
