import type { LoginResponse } from "@/types/domain";
import i18n from "@/i18n";
import { ApiError, request } from "./core";

export type CaptchaResponse = {
  id: string;
  question: string;
};

export function createAuthApi() {
  return {
    async getCaptcha(): Promise<CaptchaResponse> {
      return request<CaptchaResponse>("/auth/captcha", { method: "GET" });
    },

    async login(
      username: string,
      password: string,
      captchaId?: string,
      captchaAnswer?: string
    ): Promise<LoginResponse> {
      const body: Record<string, string> = { username, password };
      if (captchaId) body.captcha_id = captchaId;
      if (captchaAnswer) body.captcha_answer = captchaAnswer;
      const result = await request<LoginResponse>("/auth/login", {
        method: "POST",
        body
      });
      if (!result || typeof result !== "object") {
        throw new ApiError(500, i18n.t("login.errorLoginFormat"), result);
      }
      if (!("token" in result) && !("requires_2fa" in result)) {
        throw new ApiError(500, i18n.t("login.errorLoginFormat"), result);
      }
      return result;
    },

    async logout(token: string): Promise<void> {
      await request("/auth/logout", {
        method: "POST",
        token
      });
    },

    async changePassword(token: string, currentPassword: string, newPassword: string): Promise<void> {
      await request("/auth/change-password", {
        method: "POST",
        token,
        body: {
          current_password: currentPassword,
          new_password: newPassword
        }
      });
    }
  };
}
