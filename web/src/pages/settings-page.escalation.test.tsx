import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import {
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  isAuthTransitionActive,
  releaseAuthTransitionBarrier,
  rememberAuthIdentity,
} from "@/lib/api/core";
import { SettingsPageEscalation } from "./settings-page.escalation";
import type { EscalationPolicy } from "@/types/domain";

type Role = "admin" | "operator" | "viewer";
type Identity = { token: string | null; role: Role | null };
type EditorProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  policy?: EscalationPolicy;
  onSaved: (policy: EscalationPolicy) => void;
};

const { authStore, editor, toastMock, api } = vi.hoisted(() => {
  let snapshot: Identity = { token: "token-a", role: "admin" };
  const listeners = new Set<() => void>();
  return {
    // Mirrors AuthProvider: Context consumers re-render when the identity changes.
    authStore: {
      get: () => snapshot,
      set(next: Identity) {
        snapshot = next;
        for (const listener of listeners) listener();
      },
      subscribe(listener: () => void) {
        listeners.add(listener);
        return () => {
          listeners.delete(listener);
        };
      },
    },
    editor: { current: null as EditorProps | null },
    toastMock: { error: vi.fn(), success: vi.fn() },
    api: { list: vi.fn(), update: vi.fn(), remove: vi.fn() },
  };
});

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

// vi.mock factories are hoisted above static imports, so the module they need loads inside the factory.
vi.mock("@/context/auth-context.hooks", async () => {
  const { useSyncExternalStore } = await import("react");
  return { useAuth: () => useSyncExternalStore(authStore.subscribe, authStore.get) };
});

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listEscalationPolicies: api.list,
      updateEscalationPolicy: api.update,
      deleteEscalationPolicy: api.remove,
    },
  };
});

// The real editor has its own identity guard; this stub exposes the onSaved the panel handed to it.
vi.mock("@/components/escalation-policy-editor", () => ({
  EscalationPolicyEditor: (props: EditorProps) => {
    editor.current = props;
    return props.open ? <div role="dialog">editor</div> : null;
  },
}));

vi.mock("@/components/ui/toast-sonner", () => ({ toast: toastMock }));

const confirmMock = vi.fn();
vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({ confirm: confirmMock, dialog: null }),
}));

// ---- fixtures ----

const makePolicy = (overrides?: Partial<EscalationPolicy>): EscalationPolicy => ({
  id: 1,
  name: "策略A",
  description: "",
  minSeverity: "warning",
  enabled: true,
  levels: [{ delaySeconds: 0, integrationIds: [1], severityOverride: "", tags: [] }],
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-02T00:00:00Z",
  ...overrides,
});

type Deferred<T> = { promise: Promise<T>; resolve: (value: T) => void; reject: (error: unknown) => void };
// The ES2022 lib configured for web/ has no Promise.withResolvers.
function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

// AuthProvider order: advance the generation, then install Context and core identity together.
function signIn(token: string | null, role: Role | null) {
  act(() => {
    bumpAuthSessionGeneration();
    rememberAuthIdentity(token, role);
    authStore.set({ token, role });
  });
}

function downgrade(role: Role) {
  act(() => {
    rememberAuthIdentity("token-a", role);
    authStore.set({ token: "token-a", role });
  });
}

async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
}

const policyA = makePolicy({ id: 1, name: "策略A" });
const policyB = makePolicy({ id: 2, name: "身份B策略" });

/** Identity changes after which every pending completion must be discarded. */
const identityChanges: Array<[string, () => void]> = [
  ["logout", () => signIn(null, null)],
  ["downgrade to viewer", () => downgrade("viewer")],
  ["A→B→A", () => {
    signIn("token-b", "admin");
    signIn("token-a", "admin");
  }],
  ["same-token re-login", () => signIn("token-a", "admin")],
];

beforeEach(() => {
  if (isAuthTransitionActive()) clearAuthTransitionBarrier();
  vi.clearAllMocks();
  editor.current = null;
  rememberAuthIdentity("token-a", "admin");
  authStore.set({ token: "token-a", role: "admin" });
  confirmMock.mockResolvedValue(true);
  api.list.mockReset();
  api.update.mockReset();
  api.remove.mockReset();
  api.list.mockImplementation(async (token: string) => (token === "token-b" ? [policyB] : [policyA]));
});

afterEach(() => {
  if (isAuthTransitionActive()) clearAuthTransitionBarrier();
});

describe("SettingsPageEscalation", () => {
  it("renders list of policies (mock returns 2)", async () => {
    const p2 = makePolicy({ id: 2, name: "策略B", minSeverity: "critical" });
    api.list.mockResolvedValue([policyA, p2]);

    render(<SettingsPageEscalation />);

    expect(await screen.findByText("策略A")).toBeInTheDocument();
    expect(screen.getByText("策略B")).toBeInTheDocument();
    expect(screen.getAllByText("warning").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("critical")).toBeInTheDocument();
    expect(api.list).toHaveBeenCalledWith("token-a", { signal: expect.any(AbortSignal) });
  });

  it("empty state shown when list empty", async () => {
    api.list.mockResolvedValue([]);

    render(<SettingsPageEscalation />);

    expect(await screen.findByText("还没有升级策略")).toBeInTheDocument();
    expect(screen.getByText("创建策略以启用告警多级升级")).toBeInTheDocument();
  });

  it("enable toggle calls updateEscalationPolicy with new enabled and applies the result", async () => {
    const policy = makePolicy({ id: 3, name: "策略C", enabled: true });
    api.list.mockResolvedValue([policy]);
    api.update.mockResolvedValue({ ...policy, enabled: false, name: "策略C已更新" });

    render(<SettingsPageEscalation />);
    fireEvent.click(await screen.findByLabelText("启用 策略C"));

    await waitFor(() => expect(api.update).toHaveBeenCalledOnce());
    expect(api.update).toHaveBeenCalledWith("token-a", 3, expect.objectContaining({ enabled: false }));
    expect(await screen.findByText("策略C已更新")).toBeInTheDocument();
    expect(toastMock.error).not.toHaveBeenCalled();
  });

  it("delete flow: click delete → confirm → deleteEscalationPolicy called → row removed", async () => {
    const policy = makePolicy({ id: 4, name: "策略D" });
    api.list.mockResolvedValue([policy]);
    api.remove.mockResolvedValue({ deleted: true });

    render(<SettingsPageEscalation />);
    fireEvent.click(await screen.findByLabelText("删除 策略D"));

    await waitFor(() => expect(api.remove).toHaveBeenCalledWith("token-a", 4));
    await waitFor(() => expect(screen.queryByText("策略D")).toBeNull());
  });

  it("applies a save from the current editor", async () => {
    render(<SettingsPageEscalation />);
    expect(await screen.findByText("策略A")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("编辑 策略A"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    act(() => editor.current?.onSaved({ ...policyA, name: "策略A已保存" }));
    expect(screen.getByText("策略A已保存")).toBeInTheDocument();
    expect(screen.queryByText("策略A")).not.toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("button", { name: /新建策略/ })[0]);
    act(() => editor.current?.onSaved(makePolicy({ id: 9, name: "新策略" })));
    expect(screen.getByText("新策略")).toBeInTheDocument();
  });

  it.each(["operator", "viewer"] as const)("does not mount or read policies for %s", async (role) => {
    downgrade(role);
    render(<SettingsPageEscalation />);
    await settle();
    expect(screen.queryByText("升级策略")).not.toBeInTheDocument();
    expect(api.list).not.toHaveBeenCalled();
  });

  it("does not mount while Context and the core identity disagree", async () => {
    rememberAuthIdentity("token-b", "admin");
    render(<SettingsPageEscalation />);
    await settle();
    expect(screen.queryByText("升级策略")).not.toBeInTheDocument();
    expect(api.list).not.toHaveBeenCalled();
  });

  describe("identity boundary", () => {
    it.each(identityChanges)("aborts and ignores a pending list read after %s", async (_label, change) => {
      const pending = createDeferred<EscalationPolicy[]>();
      api.list.mockReturnValueOnce(pending.promise);
      render(<SettingsPageEscalation />);
      await waitFor(() => expect(api.list).toHaveBeenCalledTimes(1));
      const options: unknown = api.list.mock.calls[0]?.[1];
      const signal = options && typeof options === "object" && "signal" in options ? options.signal : null;
      if (!(signal instanceof AbortSignal)) throw new Error("list read was not abortable");

      change();
      expect(signal.aborted).toBe(true);
      await act(async () => pending.resolve([makePolicy({ id: 5, name: "旧身份策略" })]));
      await settle();
      expect(screen.queryByText("旧身份策略")).not.toBeInTheDocument();
      expect(toastMock.error).not.toHaveBeenCalled();
    });

    it("ignores a pending list failure after the identity changes", async () => {
      const pending = createDeferred<EscalationPolicy[]>();
      api.list.mockReturnValueOnce(pending.promise);
      render(<SettingsPageEscalation />);
      await waitFor(() => expect(api.list).toHaveBeenCalledTimes(1));

      signIn("token-b", "admin");
      expect(await screen.findByText("身份B策略")).toBeInTheDocument();
      await act(async () => pending.reject(new Error("old list failed")));
      await settle();
      expect(toastMock.error).not.toHaveBeenCalled();
      expect(screen.getByText("身份B策略")).toBeInTheDocument();
    });

    it.each(identityChanges)("drops a pending toggle success and failure after %s", async (_label, change) => {
      const success = createDeferred<EscalationPolicy>();
      api.update.mockReturnValueOnce(success.promise);
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("启用 策略A"));
      await waitFor(() => expect(api.update).toHaveBeenCalledTimes(1));

      change();
      await settle();
      const currentRows = screen.queryAllByRole("row").map((row) => row.textContent);
      await act(async () => success.resolve({ ...policyA, enabled: false, name: "旧回执策略" }));
      await settle();
      expect(screen.queryByText("旧回执策略")).not.toBeInTheDocument();
      expect(screen.queryAllByRole("row").map((row) => row.textContent)).toEqual(currentRows);
      expect(toastMock.error).not.toHaveBeenCalled();
      expect(api.update).toHaveBeenCalledTimes(1);
    });

    it("does not revert or toast the new list when an old toggle fails", async () => {
      const failure = createDeferred<EscalationPolicy>();
      api.update.mockReturnValueOnce(failure.promise);
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("启用 策略A"));
      await waitFor(() => expect(api.update).toHaveBeenCalledTimes(1));

      signIn("token-b", "admin");
      const switchB = await screen.findByLabelText("启用 身份B策略");
      expect(switchB).toHaveAttribute("aria-checked", "true");
      await act(async () => failure.reject(new Error("old toggle failed")));
      await settle();
      expect(toastMock.error).not.toHaveBeenCalled();
      expect(screen.getByLabelText("启用 身份B策略")).toHaveAttribute("aria-checked", "true");
      expect(screen.getByLabelText("启用 身份B策略")).not.toBeDisabled();
    });

    it.each(identityChanges)("does not delete when the confirm resolves after %s", async (_label, change) => {
      const confirmed = createDeferred<boolean>();
      confirmMock.mockReturnValueOnce(confirmed.promise);
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("删除 策略A"));
      await waitFor(() => expect(confirmMock).toHaveBeenCalledTimes(1));

      change();
      await act(async () => confirmed.resolve(true));
      await settle();
      expect(api.remove).not.toHaveBeenCalled();
      expect(toastMock.error).not.toHaveBeenCalled();
    });

    it("does not remove a row from the new list when an old delete completes", async () => {
      // Both identities can see a policy with the same id; the old receipt must not touch the new one.
      api.list.mockImplementation(async (token: string) => (token === "token-b" ? [{ ...policyB, id: 1 }] : [policyA]));
      const pending = createDeferred<{ deleted: boolean }>();
      api.remove.mockReturnValueOnce(pending.promise);
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("删除 策略A"));
      await waitFor(() => expect(api.remove).toHaveBeenCalledWith("token-a", 1));

      signIn("token-b", "admin");
      expect(await screen.findByText("身份B策略")).toBeInTheDocument();
      await act(async () => pending.resolve({ deleted: true }));
      await settle();
      expect(screen.getByText("身份B策略")).toBeInTheDocument();
      expect(screen.getByLabelText("删除 身份B策略")).not.toBeDisabled();
      expect(toastMock.error).not.toHaveBeenCalled();
    });

    it("does not toast an old delete failure", async () => {
      const pending = createDeferred<{ deleted: boolean }>();
      api.remove.mockReturnValueOnce(pending.promise);
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("删除 策略A"));
      await waitFor(() => expect(api.remove).toHaveBeenCalledTimes(1));

      signIn("token-a", "admin");
      expect(await screen.findByText("策略A")).toBeInTheDocument();
      await act(async () => pending.reject(new Error("old delete failed")));
      await settle();
      expect(toastMock.error).not.toHaveBeenCalled();
      expect(screen.getByText("策略A")).toBeInTheDocument();
      expect(screen.getByLabelText("删除 策略A")).not.toBeDisabled();
    });

    it.each(identityChanges)("ignores an old editor save after %s", async (_label, change) => {
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("编辑 策略A"));
      const staleSaved = editor.current?.onSaved;
      expect(staleSaved).toBeTypeOf("function");

      change();
      await settle();
      act(() => staleSaved?.({ ...policyA, name: "旧编辑器结果" }));
      act(() => staleSaved?.(makePolicy({ id: 77, name: "旧编辑器新增" })));
      expect(screen.queryByText("旧编辑器结果")).not.toBeInTheDocument();
      expect(screen.queryByText("旧编辑器新增")).not.toBeInTheDocument();
      expect(toastMock.success).not.toHaveBeenCalled();
    });

    it("ignores a save from an editor that was closed and reopened for another policy", async () => {
      const second = makePolicy({ id: 2, name: "策略二" });
      api.list.mockResolvedValue([policyA, second]);
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("编辑 策略A"));
      const staleSaved = editor.current?.onSaved;

      act(() => editor.current?.onOpenChange(false));
      fireEvent.click(screen.getByLabelText("编辑 策略二"));
      act(() => staleSaved?.({ ...policyA, name: "关闭后旧结果" }));
      expect(screen.queryByText("关闭后旧结果")).not.toBeInTheDocument();

      act(() => editor.current?.onSaved({ ...second, name: "策略二已保存" }));
      expect(screen.getByText("策略二已保存")).toBeInTheDocument();
    });

    it("unmounts during an auth transition and remounts with a fresh read after release", async () => {
      const pending = createDeferred<EscalationPolicy>();
      api.update.mockReturnValueOnce(pending.promise);
      render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("编辑 策略A"));
      const staleSaved = editor.current?.onSaved;
      fireEvent.click(screen.getByLabelText("启用 策略A"));
      await waitFor(() => expect(api.update).toHaveBeenCalledTimes(1));

      let barrier = 0;
      act(() => {
        barrier = beginAuthTransitionBarrier();
      });
      expect(screen.queryByText("策略A")).not.toBeInTheDocument();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      await act(async () => pending.resolve({ ...policyA, name: "屏障中回执" }));

      act(() => {
        releaseAuthTransitionBarrier(barrier);
      });
      expect(await screen.findByText("策略A")).toBeInTheDocument();
      expect(api.list).toHaveBeenCalledTimes(2);
      act(() => staleSaved?.({ ...policyA, name: "屏障前编辑器" }));
      await settle();
      expect(screen.queryByText("屏障中回执")).not.toBeInTheDocument();
      expect(screen.queryByText("屏障前编辑器")).not.toBeInTheDocument();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(toastMock.error).not.toHaveBeenCalled();
    });

    it("emits nothing when pending work settles after unmount", async () => {
      const toggle = createDeferred<EscalationPolicy>();
      const remove = createDeferred<{ deleted: boolean }>();
      api.update.mockReturnValueOnce(toggle.promise);
      api.remove.mockReturnValueOnce(remove.promise);
      const view = render(<SettingsPageEscalation />);
      fireEvent.click(await screen.findByLabelText("启用 策略A"));
      fireEvent.click(screen.getByLabelText("删除 策略A"));
      await waitFor(() => expect(api.remove).toHaveBeenCalledTimes(1));

      view.unmount();
      await act(async () => {
        toggle.reject(new Error("late toggle"));
        remove.reject(new Error("late delete"));
      });
      await settle();
      expect(toastMock.error).not.toHaveBeenCalled();
      expect(toastMock.success).not.toHaveBeenCalled();
    });
  });
});
