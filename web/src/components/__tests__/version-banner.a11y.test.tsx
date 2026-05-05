import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { axe } from "vitest-axe";

// Wave 4 PR-B：version-banner a11y smoke 测试，回归保护
// 关闭按钮 aria-label + sr-only + 装饰图标 aria-hidden 的修复。

const { checkVersionMock, useAuthMock } = vi.hoisted(() => ({
  checkVersionMock: vi.fn(),
  useAuthMock: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    checkVersion: checkVersionMock,
  },
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => useAuthMock(),
}));

import { VersionBanner } from "../version-banner";

describe("VersionBanner a11y", () => {
  beforeEach(() => {
    checkVersionMock.mockReset();
    useAuthMock.mockReset();
    useAuthMock.mockReturnValue({ token: "test-token", role: "admin" });
    window.localStorage.removeItem("xirang.dismissed-version");
  });

  it("smoke: 提示横幅渲染无 axe 违规", async () => {
    checkVersionMock.mockResolvedValue({
      current_version: "1.0.0",
      latest_version: "1.1.0",
      release_url: "https://example.com/release",
      update_available: true,
    });

    const { container } = render(<VersionBanner />);

    // 等待异步状态更新（fetch → setState 后渲染横幅）
    await waitFor(() => {
      expect(container.querySelector("[role=status]")).toBeInTheDocument();
    });

    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });
});
