import type { LoginResponse } from "@/types/domain";
import i18n from "@/i18n";
import { ApiError, request } from "./core";

export type CaptchaResponse = {
  /** True when login.captcha_enabled — only then are id/question present. */
  enabled: boolean;
  id?: string;
  question?: string;
  secondRequired?: boolean;
  secondId?: string;
  secondQuestion?: string;
};

type RawCaptchaResponse = {
  enabled?: unknown;
  id?: unknown;
  question?: unknown;
  second_required?: unknown;
  second_id?: unknown;
  second_question?: unknown;
};

export type LoginCaptchaPayload = {
  captchaId?: string;
  captchaAnswer?: string;
  secondCaptchaId?: string;
  secondCaptchaAnswer?: string;
};

export function createAuthApi() {
  return {
    async getCaptcha(): Promise<CaptchaResponse> {
      const raw = await request<RawCaptchaResponse>("/auth/captcha", { method: "GET" });
      const enabled = Boolean(raw?.enabled);
      const secondRequired = Boolean(raw?.second_required);
      return {
        enabled,
        id: enabled && typeof raw?.id === "string" ? raw.id : undefined,
        question: enabled && typeof raw?.question === "string" ? raw.question : undefined,
        secondRequired,
        secondId:
          secondRequired && typeof raw?.second_id === "string" ? raw.second_id : undefined,
        secondQuestion:
          secondRequired && typeof raw?.second_question === "string"
            ? raw.second_question
            : undefined,
      };
    },

    async login(
      username: string,
      password: string,
      captcha?: LoginCaptchaPayload | string,
      captchaAnswer?: string
    ): Promise<LoginResponse> {
      // Backward-compatible overload: login(user, pass, captchaId?, captchaAnswer?)
      // or login(user, pass, { captchaId, captchaAnswer, secondCaptchaId, secondCaptchaAnswer })
      const body: Record<string, string> = { username, password };
      if (typeof captcha === "object" && captcha !== null) {
        if (captcha.captchaId) body.captcha_id = captcha.captchaId;
        if (captcha.captchaAnswer) body.captcha_answer = captcha.captchaAnswer;
        if (captcha.secondCaptchaId) body.second_captcha_id = captcha.secondCaptchaId;
        if (captcha.secondCaptchaAnswer) body.second_captcha_answer = captcha.secondCaptchaAnswer;
      } else {
        if (captcha) body.captcha_id = captcha;
        if (captchaAnswer) body.captcha_answer = captchaAnswer;
      }
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
