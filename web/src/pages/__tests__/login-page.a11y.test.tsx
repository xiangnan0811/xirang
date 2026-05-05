import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { runAxe } from "@/test/a11y-helpers";

// Wave 4 PR-C：login 页 a11y smoke 测试。
// PR-D: 改用 runAxe 共享辅助（默认关闭 color-contrast，详见 a11y-helpers.ts）。

const { navigateMock, loginMock, apiLoginMock, apiTotpLoginMock, getCaptchaMock } = vi.hoisted(() => ({
  navigateMock: vi.fn(),
  loginMock: vi.fn(),
  apiLoginMock: vi.fn(),
  apiTotpLoginMock: vi.fn(),
  getCaptchaMock: vi.fn(),
}));

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useNavigate: () => navigateMock,
  };
});

vi.mock("@/lib/api/client", () => ({
  ApiError: class ApiError extends Error {
    status: number;
    detail?: unknown;
    constructor(status: number, message: string, detail?: unknown) {
      super(message);
      this.name = "ApiError";
      this.status = status;
      this.detail = detail;
    }
  },
  apiClient: {
    login: apiLoginMock,
    totpLogin: apiTotpLoginMock,
    getCaptcha: getCaptchaMock,
  },
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    isAuthenticated: false,
    login: loginMock,
  }),
}));

import { LoginPage } from "../login-page";

describe("LoginPage a11y smoke", () => {
  beforeEach(() => {
    navigateMock.mockReset();
    loginMock.mockReset();
    apiLoginMock.mockReset();
    apiTotpLoginMock.mockReset();
    getCaptchaMock.mockReset();
    // 默认验证码接口不可用，避免 captcha 块 mock 复杂度
    getCaptchaMock.mockRejectedValue(new Error("captcha disabled"));
  });

  it("初始渲染无 axe violations（关 color-contrast）", async () => {
    const { container } = render(
      <MemoryRouter
        initialEntries={["/login"]}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <LoginPage />
      </MemoryRouter>
    );

    // 等首轮 useEffect (fetchCaptcha) 完成，避免 act 警告影响 axe
    await waitFor(() => {
      expect(getCaptchaMock).toHaveBeenCalled();
    });

    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });
});
