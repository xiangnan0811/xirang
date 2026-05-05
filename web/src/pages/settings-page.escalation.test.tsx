import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { SettingsPageEscalation } from "./settings-page.escalation";
import type { EscalationPolicy } from "@/types/domain";

// ---- mocks ----

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      const map: Record<string, string> = {
        "escalation.tabTitle": "升级策略",
        "escalation.newButton": "新建策略",
        "escalation.empty.title": "还没有升级策略",
        "escalation.empty.hint": "创建策略以启用告警多级升级",
        "escalation.fields.name": "名称",
        "escalation.fields.minSeverity": "最低生效严重度",
        "escalation.fields.levels": "升级级别",
        "escalation.fields.enabled": "启用",
        "escalation.deleteConfirm": `确定要删除策略「${String(opts?.name ?? "")}」吗？`,
      };
      return map[key] ?? key;
    },
  }),
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

const mockList = vi.fn();
const mockUpdate = vi.fn();
const mockDelete = vi.fn();

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listEscalationPolicies: (...args: unknown[]) => mockList(...args),
      updateEscalationPolicy: (...args: unknown[]) => mockUpdate(...args),
      deleteEscalationPolicy: (...args: unknown[]) => mockDelete(...args),
    },
  };
});

vi.mock("@/components/escalation-policy-editor", () => ({
  EscalationPolicyEditor: () => null,
}));

vi.mock("@/components/ui/toast", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

// ---- fixtures ----

const makePolicy = (overrides?: Partial<EscalationPolicy>): EscalationPolicy => ({
  id: 1,
  name: "策略A",
  description: "",
  min_severity: "warning",
  enabled: true,
  levels: [{ delay_seconds: 0, integration_ids: [1], severity_override: "", tags: [] }],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-02T00:00:00Z",
  ...overrides,
});

beforeEach(() => {
  vi.clearAllMocks();
  // Suppress window.confirm in tests
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

describe("SettingsPageEscalation", () => {
  it("renders list of policies (mock returns 2)", async () => {
    const p1 = makePolicy({ id: 1, name: "策略A" });
    const p2 = makePolicy({ id: 2, name: "策略B", min_severity: "critical" });
    mockList.mockResolvedValue([p1, p2]);

    render(<SettingsPageEscalation />);

    await waitFor(() => screen.getByText("策略A"));
    expect(screen.getByText("策略B")).toBeTruthy();
    expect(screen.getByText("warning")).toBeTruthy();
    expect(screen.getByText("critical")).toBeTruthy();
  });

  it("empty state shown when list empty", async () => {
    mockList.mockResolvedValue([]);

    render(<SettingsPageEscalation />);

    await waitFor(() => screen.getByText("还没有升级策略"));
    expect(screen.getByText("创建策略以启用告警多级升级")).toBeTruthy();
  });

  it("enable toggle calls updateEscalationPolicy with new enabled", async () => {
    const policy = makePolicy({ id: 3, name: "策略C", enabled: true });
    mockList.mockResolvedValue([policy]);
    mockUpdate.mockResolvedValue({ ...policy, enabled: false });

    render(<SettingsPageEscalation />);

    await waitFor(() => screen.getByText("策略C"));

    const toggle = screen.getByLabelText("启用 策略C");
    fireEvent.click(toggle);

    await waitFor(() => expect(mockUpdate).toHaveBeenCalledOnce());
    const [, id, input] = mockUpdate.mock.calls[0] as [string, number, { enabled: boolean }];
    expect(id).toBe(3);
    expect(input.enabled).toBe(false);
  });

  it("delete flow: click delete → confirm → deleteEscalationPolicy called → row removed", async () => {
    const policy = makePolicy({ id: 4, name: "策略D" });
    mockList.mockResolvedValue([policy]);
    mockDelete.mockResolvedValue({ deleted: true });

    render(<SettingsPageEscalation />);

    await waitFor(() => screen.getByText("策略D"));

    const deleteBtn = screen.getByLabelText("删除 策略D");
    fireEvent.click(deleteBtn);

    await waitFor(() => expect(mockDelete).toHaveBeenCalledWith("test-token", 4));
    await waitFor(() => expect(screen.queryByText("策略D")).toBeNull());
  });
});
