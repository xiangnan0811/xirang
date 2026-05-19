import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { SystemTab } from "./settings-page.system";

const apiClientMock = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getLogsSettings: vi.fn(),
  getSecurityRiskSummary: vi.fn(),
  updateLogsSettings: vi.fn(),
  updateSettings: vi.fn(),
  resetSetting: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (typeof options?.count === "number") {
        return `${key}:${options.count}`;
      }
      if (typeof options?.defaultValue === "string") {
        return options.defaultValue;
      }
      return key;
    },
  }),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

describe("SystemTab security risk summary", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiClientMock.getSettings.mockResolvedValue({ definitions: [], values: {} });
    apiClientMock.getLogsSettings.mockResolvedValue({ default_retention_days: 30 });
    apiClientMock.getSecurityRiskSummary.mockResolvedValue({
      generatedAt: "2026-05-18T00:00:00Z",
      summary: { totalRisks: 4, categories: 4 },
      items: [
        {
          code: "root_ssh_users",
          severity: "warning",
          title: "Root SSH users",
          description: "root nodes",
          count: 2,
          examples: ["node-a", "node-b"],
        },
        {
          code: "broad_scope_ssh_keys",
          severity: "warning",
          title: "Broad scope SSH keys",
          description: "broad keys",
          count: 1,
          examples: ["ops-key"],
        },
        {
          code: "weak_security_defaults",
          severity: "info",
          title: "Weak defaults",
          description: "defaults",
          count: 0,
          examples: [],
        },
      ],
    });
  });

  it("renders advisory risk cards without remediation links", async () => {
    render(<SystemTab />);

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "settings.system.securityRisk.title" })).toBeInTheDocument();
    });
    expect(apiClientMock.getSecurityRiskSummary).toHaveBeenCalledWith("test-token");
    expect(screen.getByRole("heading", { name: "Root SSH users" })).toBeInTheDocument();
    expect(screen.getByText("settings.system.securityRisk.count:2")).toBeInTheDocument();
    expect(screen.getByText("node-a")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Broad scope SSH keys" })).toBeInTheDocument();
    expect(screen.getByText("ops-key")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Weak defaults" })).toBeInTheDocument();
    expect(screen.getByText("settings.system.securityRisk.noExamples")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
