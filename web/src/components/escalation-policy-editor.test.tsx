import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { EscalationPolicyEditor } from "./escalation-policy-editor";
import type { EscalationPolicy } from "@/types/domain";
import {
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  rememberAuthIdentity,
} from "@/lib/api/core";

// ---- mocks ----

const mockGetIntegrations = vi.fn();

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      const map: Record<string, string> = {
        "escalation.newButton": "新建策略",
        "escalation.fields.name": "名称",
        "escalation.fields.description": "描述",
        "escalation.fields.minSeverity": "最低生效严重度",
        "escalation.fields.enabled": "启用",
        "escalation.fields.levels": "升级级别",
        "escalation.levels.level": `第 ${(opts?.n as number) ?? "?"} 级`,
        "escalation.levels.delaySeconds": "延迟 (秒)",
        "escalation.levels.delayHint": "首级固定为 0",
        "escalation.levels.integrations": "通知通道",
        "escalation.levels.severityOverride": "严重度调整",
        "escalation.levels.tags": "打标签",
        "escalation.levels.tagsHint": "每级最多 10 个",
        "escalation.levels.addLevel": "添加下一级",
        "escalation.levels.removeLevel": "删除此级",
        "escalation.severity.info": "info",
        "escalation.severity.warning": "warning",
        "escalation.severity.critical": "critical",
        "escalation.severityOverride.empty": "保持原严重度",
        "escalation.validation.nameRequired": "名称不能为空",
        "escalation.validation.nameTooLong": "名称不能超过 100 字符",
        "escalation.validation.firstDelayMustBeZero": "首级延迟必须为 0",
        "escalation.validation.delayMustIncrease": "延迟必须严格递增",
        "escalation.validation.integrationsRequired": "每级至少选择 1 个通道",
        "escalation.validation.tooManyLevels": "级别最多 5 个",
        "escalation.validation.tagTooLong": "单个 tag 最长 32 字符",
        "escalation.validation.tooManyTags": "每级最多 10 个 tag",
        "escalation.errors.conflict": "升级策略名称已存在",
        "escalation.actions.cancel": "取消",
        "escalation.actions.save": "保存",
        "escalation.actions.saving": "保存中…",
        "escalation.levels.noIntegrations": "暂无可用通道",
        "escalation.preview.draftTitle": "预演（不保存）",
        "escalation.preview.draftHint": "测试升级路径与渠道匹配",
        "escalation.preview.action": "预演升级",
        "escalation.preview.failed": "加载失败",
        "escalation.preview.retryAction": "重试",
      };
      return map[key] ?? key;
    },
  }),
}));

vi.mock("@/lib/api/integrations-api", () => ({
  createIntegrationsApi: () => ({
    getIntegrations: (...args: unknown[]) => mockGetIntegrations(...args),
  }),
}));

const mockCreate = vi.fn();
const mockUpdate = vi.fn();

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      createEscalationPolicy: (...args: unknown[]) => mockCreate(...args),
      updateEscalationPolicy: (...args: unknown[]) => mockUpdate(...args),
    },
  };
});

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token", role: "admin" }),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

// ---- helpers ----

const samplePolicy: EscalationPolicy = {
  id: 7,
  name: "测试策略",
  description: "一段描述",
  minSeverity: "critical",
  enabled: false,
  levels: [
    { delaySeconds: 0, integrationIds: [1], severityOverride: "critical", tags: ["urgent"] },
    { delaySeconds: 300, integrationIds: [1], severityOverride: "", tags: [] },
  ],
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
};

function renderEditor(props?: Partial<Parameters<typeof EscalationPolicyEditor>[0]>) {
  const defaults = {
    open: true,
    onOpenChange: vi.fn(),
    onSaved: vi.fn(),
  };
  return render(<EscalationPolicyEditor {...defaults} {...props} />);
}

beforeEach(() => {
  vi.clearAllMocks();
  clearAuthTransitionBarrier();
  rememberAuthIdentity("test-token", "admin");
  mockGetIntegrations.mockResolvedValue([
    {
      id: "int-1",
      type: "email",
      name: "Email通道",
      endpoint: "smtp://x",
      hasSecret: false,
      enabled: true,
      failThreshold: 3,
      cooldownMinutes: 5,
      proxyUrl: "",
    },
  ]);
});

describe("EscalationPolicyEditor", () => {
  it("create mode: fill name, add second level, save → createEscalationPolicy called with correct shape; onSaved receives returned policy", async () => {
    const returnedPolicy: EscalationPolicy = { ...samplePolicy, id: 99, name: "新策略" };
    mockCreate.mockResolvedValue(returnedPolicy);

    const onSaved = vi.fn();
    renderEditor({ onSaved });

    // Fill name
    const nameInput = screen.getByLabelText("名称") as HTMLInputElement;
    fireEvent.change(nameInput, { target: { value: "新策略" } });

    // Wait for integrations to load then select integration for level 1
    await waitFor(() => screen.getByText("Email通道"));
    fireEvent.click(screen.getByText("Email通道"));

    // Add second level
    fireEvent.click(screen.getByText("添加下一级"));

    // Select integration for level 2 (second group of toggle buttons)
    const intButtons = screen.getAllByText("Email通道");
    fireEvent.click(intButtons[1]);

    // Click Save
    const saveButton = screen.getByText("保存");
    await waitFor(() => expect(saveButton).not.toBeDisabled());
    fireEvent.click(saveButton);

    await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce());
    const [calledToken, calledInput] = mockCreate.mock.calls[0] as [string, unknown];
    expect(calledToken).toBe("test-token");
    expect(calledInput).toMatchObject({
      name: "新策略",
      levels: expect.arrayContaining([
        expect.objectContaining({ delaySeconds: 0, integrationIds: [1] }),
      ]),
    });
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(returnedPolicy));
  });

  it("edit mode: policy prop pre-fills all fields", async () => {
    renderEditor({ policy: samplePolicy });

    // Wait for dialog body to render
    await waitFor(() => {
      const nameInput = screen.getByLabelText("名称") as HTMLInputElement;
      expect(nameInput.value).toBe("测试策略");
    });

    // Description
    const descEl = screen.getByDisplayValue("一段描述");
    expect(descEl).toBeTruthy();

    // Two level rows
    expect(screen.getByText("第 1 级")).toBeTruthy();
    expect(screen.getByText("第 2 级")).toBeTruthy();
  });

  it("validation: empty name blocks Save button", async () => {
    renderEditor();

    await waitFor(() => screen.getByText("Email通道"));
    fireEvent.click(screen.getByText("Email通道"));

    const saveButton = screen.getByText("保存");
    expect(saveButton).toBeDisabled();

    const nameInput = screen.getByLabelText("名称") as HTMLInputElement;
    fireEvent.change(nameInput, { target: { value: "ok" } });

    await waitFor(() => expect(saveButton).not.toBeDisabled());
  });

  it("max levels: '+ Add level' button disabled at 5 levels", async () => {
    renderEditor();

    await waitFor(() => screen.getByText("Email通道"));

    const addBtn = screen.getByText("添加下一级");
    fireEvent.click(addBtn);
    fireEvent.click(addBtn);
    fireEvent.click(addBtn);
    fireEvent.click(addBtn);

    await waitFor(() => {
      expect(screen.getByText("添加下一级")).toBeDisabled();
    });
  });

  it("renders draft preview trigger section", async () => {
    renderEditor();
    expect(screen.getByText("预演（不保存）")).toBeInTheDocument();
  });

  it("handles integration fetch failure with retry", async () => {
    mockGetIntegrations.mockRejectedValueOnce(new Error("Network error"));
    renderEditor();

    await waitFor(() => {
      expect(screen.getByText("重试")).toBeInTheDocument();
    });

    mockGetIntegrations.mockResolvedValueOnce([
      {
        id: "int-1",
        type: "email",
        name: "Email通道",
        endpoint: "smtp://x",
        hasSecret: false,
        enabled: true,
      },
    ]);

    fireEvent.click(screen.getByText("重试"));

    await waitFor(() => {
      expect(screen.getByText("Email通道")).toBeInTheDocument();
    });
  });

  it("closes dialog when auth transition is triggered (A-B-A fence)", async () => {
    const onOpenChange = vi.fn();
    renderEditor({ onOpenChange });

    beginAuthTransitionBarrier();

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });

  it("closes dialog when auth session generation is bumped", async () => {
    const onOpenChange = vi.fn();
    renderEditor({ onOpenChange });

    bumpAuthSessionGeneration();

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });

  it("handles 409 name conflict on save without blocking draft preview", async () => {
    mockCreate.mockRejectedValue(new Error("409 conflict"));
    renderEditor();

    await waitFor(() => screen.getByText("Email通道"));
    fireEvent.click(screen.getByText("Email通道"));

    const nameInput = screen.getByLabelText("名称") as HTMLInputElement;
    fireEvent.change(nameInput, { target: { value: "同名策略" } });

    const saveButton = screen.getByText("保存");
    await waitFor(() => expect(saveButton).not.toBeDisabled());
    fireEvent.click(saveButton);

    await waitFor(() => {
      expect(screen.getByText("升级策略名称已存在")).toBeInTheDocument();
    });

    // Save button is disabled due to conflict, but draft preview trigger is still present and valid
    expect(saveButton).toBeDisabled();
    expect(screen.getByText("预演（不保存）")).toBeInTheDocument();
  });

  it("does not invoke onSaved if save completes after unmount/transition", async () => {
    let resolveSave!: (value: EscalationPolicy) => void;
    mockCreate.mockReturnValue(new Promise((resolve) => {
      resolveSave = resolve;
    }));

    const onSaved = vi.fn();
    const { unmount } = renderEditor({ onSaved });

    await waitFor(() => screen.getByText("Email通道"));
    fireEvent.click(screen.getByText("Email通道"));

    const nameInput = screen.getByLabelText("名称") as HTMLInputElement;
    fireEvent.change(nameInput, { target: { value: "新策略" } });

    const saveButton = screen.getByText("保存");
    await waitFor(() => expect(saveButton).not.toBeDisabled());
    fireEvent.click(saveButton);

    // Unmount before save promise resolves
    unmount();

    // Late resolution
    resolveSave({ ...samplePolicy, id: 99, name: "新策略" });
    await waitFor(() => {
      expect(onSaved).not.toHaveBeenCalled();
    });
  });

  describe("draft preview, save and identity fences", () => {
    type Pending = { url: string; signal: AbortSignal; resolve: (response: Response) => void };
    let fetches: Pending[] = [];

    const draftResponse = {
      mode: "draft",
      basis: "current_snapshot",
      generated_at: "2026-10-18T10:00:00Z",
      expires_at: "2026-10-18T10:01:00Z",
      evaluation_at: "2026-10-18T10:00:00Z",
      tick_seconds: 30,
      requested_alert_id: null,
      status: "projected",
      policy_source: "draft",
      source_resource_id: null,
      policy: { id: null, name: "新策略", min_severity: "warning", enabled: true, updated_at: null },
      alert: { id: null, node_id: 0, severity: "warning", status: "open", triggered_at: "2026-10-18T10:00:00Z", last_level_fired: -1, tags: [] },
      next_level_index: 0,
      next_due_at: "2026-10-18T10:00:00Z",
      next_due_now: true,
      delivery_decision: null,
      delivery_counts: null,
      warnings: [],
      steps: [
        {
          level_index: 0,
          delay_seconds: 0,
          due_at: "2026-10-18T10:00:00Z",
          projected_at: "2026-10-18T10:00:00Z",
          status: "would_enqueue",
          severity_before: "warning",
          severity_after: "warning",
          tags_after: [],
          matched_silence_ids: [],
          channels: [{ id: 1, name: "Ops Webhook", type: "webhook", state: "enabled" }],
        },
      ],
    };

    async function flush() {
      for (let index = 0; index < 6; index += 1) {
        await act(async () => {
          await new Promise<void>((resolve) => setImmediate(resolve));
        });
      }
    }

    /** Fills a valid, savable draft and a valid preview scenario. */
    async function fillDraft(name = "新策略") {
      await waitFor(() => screen.getByText("Email通道"));
      fireEvent.click(screen.getByText("Email通道"));
      fireEvent.change(screen.getByLabelText("名称"), { target: { value: name } });
      fireEvent.click(screen.getByText("预演（不保存）"));
      fireEvent.change(screen.getByLabelText("escalation.preview.scenario.errorCode"), { target: { value: "XR-EXEC-7" } });
    }

    async function previewToReady() {
      fireEvent.click(screen.getByRole("button", { name: "预演升级" }));
      const previewFetch = () => fetches.find((entry) => entry.url.includes("/escalation-policies/preview"));
      await waitFor(() => expect(previewFetch()).toBeDefined());
      const call = previewFetch();
      call!.resolve(
        new Response(JSON.stringify({ code: 200, message: "ok", data: draftResponse }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      );
      await flush();
      expect(screen.getByText("Ops Webhook")).toBeInTheDocument();
    }

    beforeEach(() => {
      fetches = [];
      vi.stubGlobal(
        "fetch",
        vi.fn(
          (input: RequestInfo | URL, init: RequestInit = {}) =>
            new Promise<Response>((resolve) => {
              fetches.push({ url: String(input), signal: init.signal as AbortSignal, resolve });
            })
        )
      );
    });

    afterEach(() => {
      vi.unstubAllGlobals();
    });

    it("clears a ready preview the moment saving starts and keeps it disabled while saving", async () => {
      mockCreate.mockReturnValue(new Promise(() => {}));
      renderEditor();
      await fillDraft();
      await previewToReady();

      fireEvent.click(screen.getByText("保存"));
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "预演升级" })).toBeDisabled();
      expect(mockCreate).toHaveBeenCalledOnce();
    });

    it("aborts an in-flight preview when saving starts", async () => {
      mockCreate.mockReturnValue(new Promise(() => {}));
      renderEditor();
      await fillDraft();
      fireEvent.click(screen.getByRole("button", { name: "预演升级" }));
      const previewFetch = () => fetches.find((entry) => entry.url.includes("/escalation-policies/preview"));
      await waitFor(() => expect(previewFetch()).toBeDefined());
      const call = previewFetch()!;
      fireEvent.click(screen.getByText("保存"));
      expect(call.signal.aborted).toBe(true);
    });

    it("allows previewing a structurally valid draft whose name conflicts on save", async () => {
      mockCreate.mockRejectedValue(new Error("409 conflict"));
      renderEditor();
      await fillDraft("同名策略");
      fireEvent.click(screen.getByText("保存"));
      await waitFor(() => expect(screen.getByText("升级策略名称已存在")).toBeInTheDocument());

      expect(screen.getByText("保存")).toBeDisabled();
      expect(screen.getByRole("button", { name: "预演升级" })).not.toBeDisabled();
      await previewToReady();
    });

    it("discards the preview when the editor is closed and reopened", async () => {
      const view = renderEditor();
      await fillDraft();
      await previewToReady();
      view.rerender(<EscalationPolicyEditor open={false} onOpenChange={vi.fn()} onSaved={vi.fn()} />);
      view.rerender(<EscalationPolicyEditor open onOpenChange={vi.fn()} onSaved={vi.fn()} />);
      await flush();
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect((screen.getByLabelText("名称") as HTMLInputElement).value).toBe("");
    });

    it("never shows the empty-inventory label while channels load, but does for a successful empty list", async () => {
      let resolveIntegrations!: (value: unknown) => void;
      mockGetIntegrations.mockReturnValueOnce(new Promise((resolve) => (resolveIntegrations = resolve)));
      renderEditor();
      expect(screen.getByRole("status")).toHaveTextContent("escalation.preview.integrationsLoading");
      expect(screen.queryByText("暂无可用通道")).not.toBeInTheDocument();
      await act(async () => {
        resolveIntegrations([]);
      });
      await waitFor(() => expect(screen.getByText("暂无可用通道")).toBeInTheDocument());
    });

    it("shows an explicit channel load failure instead of an empty choice and blocks preview until retry", async () => {
      mockGetIntegrations.mockRejectedValueOnce(new Error("Network error"));
      renderEditor();
      await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("escalation.preview.integrationsFailed"));
      expect(screen.queryByText("暂无可用通道")).not.toBeInTheDocument();
      fireEvent.click(screen.getByText("预演（不保存）"));
      expect(screen.getByText("escalation.preview.integrationsUnavailable")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "预演升级" })).toBeDisabled();

      fireEvent.click(screen.getByText("重试"));
      await waitFor(() => expect(screen.getByText("Email通道")).toBeInTheDocument());
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("keeps an in-flight channel load across parent re-renders with a new onOpenChange", async () => {
      let resolveIntegrations!: (value: unknown) => void;
      mockGetIntegrations.mockReturnValueOnce(new Promise((resolve) => (resolveIntegrations = resolve)));
      const view = renderEditor();
      view.rerender(<EscalationPolicyEditor open onOpenChange={vi.fn()} onSaved={vi.fn()} />);
      await act(async () => {
        resolveIntegrations([{ id: "int-1", type: "email", name: "Email通道", endpoint: "", hasSecret: false, enabled: true }]);
      });
      await waitFor(() => expect(screen.getByText("Email通道")).toBeInTheDocument());
      expect(mockGetIntegrations).toHaveBeenCalledTimes(1);
    });

    it("ignores a channel list that resolves after an identity change despite ignoring abort", async () => {
      let resolveStale!: (value: unknown) => void;
      mockGetIntegrations
        .mockReturnValueOnce(new Promise((resolve) => (resolveStale = resolve)))
        .mockReturnValueOnce(new Promise(() => {}));
      renderEditor();
      act(() => {
        bumpAuthSessionGeneration();
      });
      await act(async () => {
        resolveStale([{ id: "int-9", type: "email", name: "Stale通道", endpoint: "", hasSecret: false, enabled: true }]);
      });
      await flush();
      expect(screen.queryByText("Stale通道")).not.toBeInTheDocument();
    });

    it("never reports a save that resolves after an A→B→A identity change", async () => {
      let resolveSave!: (value: EscalationPolicy) => void;
      mockCreate.mockReturnValue(new Promise((resolve) => (resolveSave = resolve)));
      const onSaved = vi.fn();
      const onOpenChange = vi.fn();
      renderEditor({ onSaved, onOpenChange });
      await fillDraft();
      fireEvent.click(screen.getByText("保存"));

      act(() => {
        bumpAuthSessionGeneration();
      });
      act(() => {
        bumpAuthSessionGeneration();
      });
      await act(async () => {
        resolveSave({ ...samplePolicy, id: 99, name: "新策略" });
      });
      await flush();
      expect(onSaved).not.toHaveBeenCalled();
      expect(onOpenChange).toHaveBeenCalledWith(false);
      expect((screen.getByLabelText("名称") as HTMLInputElement).value).toBe("");
    });
  });
});
