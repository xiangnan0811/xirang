import type { LoginResponse } from "@/types/domain";
import { ApiError, request } from "./core";

export function createAuthApi() {
  return {
    async login(username: string, password: string): Promise<LoginResponse> {
      const result = await request<LoginResponse>("/auth/login", {
        method: "POST",
        body: { username, password }
      });
      if (!result || typeof result !== "object" || !("token" in result)) {
        throw new ApiError(500, "登录响应格式异常", result);
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
