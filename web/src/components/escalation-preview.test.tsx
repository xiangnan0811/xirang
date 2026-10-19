import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import {
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  rememberAuthIdentity,
  releaseAuthTransitionBarrier,
} from "@/lib/api/core";
import type { EscalationPolicyInput } from "@/lib/api/escalation";
import { AlertEscalationPreview, EscalationDraftPreview, type EscalationDraftPreviewProps } from "./escalation-preview";

const { authState } = vi.hoisted(() => ({
  authState: {
    token: "tok-admin" as string | null,
    role: "admin" as "admin" | "operator" | "viewer" | null,
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: authState.token, role: authState.role }),
}));

type PendingFetch = {
  url: string;
  init: RequestInit;
  signal: AbortSignal;
  resolve: (response: Response) => void;
  reject: (reason: unknown) => void;
};

let calls: PendingFetch[] = [];
let honorAbort = true;
let monotonicNow = 0;

const fetchMock = vi.fn(
  (input: RequestInfo | URL, init: RequestInit = {}) =>
    new Promise<Response>((resolve, reject) => {
      const signal = init.signal as AbortSignal;
      calls.push({ url: String(input), init, signal, resolve, reject });
      if (honorAbort && signal) {
        signal.addEventListener("abort", () => reject(new DOMException("The operation was aborted.", "AbortError")), {
          once: true,
        });
      }
    })
);

function envelope(data: unknown): Response {
  return new Response(JSON.stringify({ code: 200, message: "ok", data }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function failure(status: number): Response {
  return new Response(JSON.stringify({ code: status, message: "server error", data: null }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const DRAFT_TOGGLE = /预演（不保存）|Preview \(Without Saving\)/;
const RUN = /^(预演升级|Preview Escalation|重新预演|Preview Again)$/;
const DIAGNOSE = /^(诊断升级（只读）|Diagnose Escalation \(Read-only\)|重新诊断|Diagnose Again)$/;
const ERROR_CODE = /告警错误码|Alert Error Code/;
const IDLE = /配置草稿及测试场景后点击预演|Configure draft and test scenario/;
const EXPIRED = /已过期，请重新预演|Preview expired, please re-run/;
const FAILED = /升级预演请求失败，请重试|Escalation preview request failed, please retry/;

const sampleDraftPolicy: EscalationPolicyInput = {
  name: "Prod Escalation",
  description: "Core escalation policy",
  minSeverity: "warning",
  enabled: true,
  levels: [
    { delaySeconds: 0, integrationIds: [1], severityOverride: "critical", tags: ["urgent"] },
    { delaySeconds: 300, integrationIds: [2], severityOverride: "", tags: [] },
  ],
};

const validDraftResponse = {
  mode: "draft",
  basis: "current_snapshot",
  generated_at: "2026-10-18T10:00:00.25Z",
  expires_at: "2026-10-18T10:01:00.25Z",
  evaluation_at: "2026-10-18T10:00:00Z",
  tick_seconds: 30,
  requested_alert_id: null,
  status: "projected",
  policy_source: "draft",
  source_resource_id: null,
  policy: { id: null, name: "Prod Escalation", min_severity: "warning", enabled: true, updated_at: null },
  alert: {
    id: null,
    node_id: 1,
    severity: "warning",
    status: "open",
    triggered_at: "2026-10-18T10:00:00Z",
    last_level_fired: -1,
    tags: ["prod"],
  },
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
      severity_after: "critical",
      tags_after: ["prod", "urgent"],
      matched_silence_ids: [],
      channels: [{ id: 1, name: "Ops Webhook", type: "webhook", state: "enabled" }],
    },
    {
      level_index: 1,
      delay_seconds: 300,
      due_at: "2026-10-18T10:05:00Z",
      projected_at: "2026-10-18T10:05:00Z",
      status: "silenced",
      severity_before: "critical",
      severity_after: "critical",
      tags_after: ["prod", "urgent"],
      matched_silence_ids: [7, 12],
      channels: [{ id: 2, name: "Slack Channel", type: "slack", state: "disabled" }],
    },
  ],
};

const validAlertResponse = {
  mode: "alert",
  basis: "current_snapshot",
  generated_at: "2026-10-18T10:00:00Z",
  expires_at: "2026-10-18T10:01:00Z",
  evaluation_at: "2026-10-18T10:00:00Z",
  tick_seconds: 30,
  requested_alert_id: 42,
  status: "projected",
  policy_source: "task",
  source_resource_id: 88,
  policy: { id: 10, name: "Task Policy", min_severity: "warning", enabled: true, updated_at: "2026-10-17T12:00:00Z" },
  alert: {
    id: 42,
    node_id: 3,
    severity: "warning",
    status: "open",
    triggered_at: "2026-10-18T09:00:00Z",
    last_level_fired: 0,
    tags: ["cluster-a"],
  },
  next_level_index: 1,
  next_due_at: "2026-10-18T09:05:00Z",
  next_due_now: true,
  delivery_decision: "escalated",
  delivery_counts: { pending: 0, sending: 0, retrying: 0, sent: 2, failed: 1, unknown: 3 },
  warnings: ["policy_changed_since_history"],
  steps: [
    {
      level_index: 1,
      delay_seconds: 300,
      due_at: "2026-10-18T09:05:00Z",
      projected_at: "2026-10-18T10:00:00Z",
      status: "no_enabled_channels",
      severity_before: "warning",
      severity_after: "warning",
      tags_after: ["cluster-a"],
      matched_silence_ids: [],
      channels: [{ id: 9, name: null, type: null, state: "missing" }],
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

async function respond(index: number, response: Response) {
  calls[index].resolve(response);
  await flush();
}

function draftUi(props: Partial<EscalationDraftPreviewProps> = {}) {
  return <EscalationDraftPreview policy={sampleDraftPolicy} token="tok-admin" active {...props} />;
}

/** Renders, expands the drawer and fills the only required scenario field without requesting. */
function openDraft(props: Partial<EscalationDraftPreviewProps> = {}) {
  const view = render(draftUi(props));
  fireEvent.click(screen.getByRole("button", { name: DRAFT_TOGGLE }));
  fireEvent.change(screen.getByLabelText(ERROR_CODE), { target: { value: "XR-TASK-001" } });
  return view;
}

const runButton = () => screen.getByRole("button", { name: RUN });
const statusRegion = () => screen.getByRole("status");

async function runToReady() {
  fireEvent.click(runButton());
  await flush();
  await respond(calls.length - 1, envelope(validDraftResponse));
  expect(screen.getByText("Ops Webhook")).toBeInTheDocument();
}

function requestBody(index: number) {
  return JSON.parse(String(calls[index].init.body)) as { scenario: Record<string, unknown>; policy: Record<string, unknown> };
}

describe("EscalationPreview components", () => {
  // Fake-timer tests cannot poll with waitFor, so the lazy decoder module is loaded once up front; the
  // component still performs its own dynamic import and ownership checks on every click.
  beforeAll(async () => {
    await import("@/lib/api/escalation-preview");
  });

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    vi.spyOn(performance, "now").mockImplementation(() => monotonicNow);
    calls = [];
    honorAbort = true;
    monotonicNow = 0;
    authState.token = "tok-admin";
    authState.role = "admin";
    rememberAuthIdentity("tok-admin", "admin");
    clearAuthTransitionBarrier();
    bumpAuthSessionGeneration();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    clearAuthTransitionBarrier();
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  });

  describe("EscalationDraftPreview", () => {
    it.each([["en", "Alert Tags"], ["zh", "告警标签"]])("labels scenario tags in %s", async (language, label) => {
      const previousLanguage = i18n.language;
      try {
        await i18n.changeLanguage(language);
        openDraft();
        expect(screen.getByRole("textbox", { name: label })).toHaveAccessibleName(label);
      } finally {
        cleanup();
        await i18n.changeLanguage(previousLanguage);
      }
    });

    it("never requests on expand and posts the UTC scenario only on explicit click", async () => {
      openDraft();
      fireEvent.change(screen.getByLabelText(/告警触发时间|Alert Triggered Time/), { target: { value: "2026-10-18T10:00:00" } });
      fireEvent.change(screen.getByLabelText(/假设扫描时间|Assumed Scan Time/), { target: { value: "2026-10-18T10:20:30" } });
      await flush();
      expect(calls).toHaveLength(0);

      fireEvent.click(runButton());
      await waitFor(() => expect(calls).toHaveLength(1));
      expect(calls[0].url).toContain("/api/v1/escalation-policies/preview");
      expect(calls[0].init.method).toBe("POST");
      expect(requestBody(0).scenario).toEqual({
        node_id: 0,
        error_code: "XR-TASK-001",
        severity: "warning",
        tags: [],
        triggered_at: "2026-10-18T10:00:00Z",
        evaluation_at: "2026-10-18T10:20:30Z",
      });

      await respond(0, envelope(validDraftResponse));
      const region = statusRegion();
      expect(region).toHaveTextContent("Prod Escalation");
      expect(region).toHaveTextContent("2026-10-18T10:05:00Z");
      expect(region).toHaveTextContent(/#7, #12/);
      expect(screen.getByRole("list", { name: /投射的升级级别|Projected escalation levels/ })).toBeInTheDocument();
      expect(screen.getByText("Slack Channel")).toBeInTheDocument();
    });

    it("keeps years 1-99 as typed instead of remapping them to 19xx", async () => {
      openDraft();
      fireEvent.change(screen.getByLabelText(/告警触发时间|Alert Triggered Time/), { target: { value: "0050-01-02T03:04:05" } });
      fireEvent.change(screen.getByLabelText(/假设扫描时间|Assumed Scan Time/), { target: { value: "0050-01-02T03:04:05" } });
      fireEvent.click(runButton());
      await waitFor(() => expect(calls).toHaveLength(1));
      expect(requestBody(0).scenario.triggered_at).toBe("0050-01-02T03:04:05Z");
      expect(requestBody(0).scenario.evaluation_at).toBe("0050-01-02T03:04:05Z");
    });

    it("rejects sub-second scan times because the scenario uses whole UTC seconds", () => {
      openDraft();
      fireEvent.change(screen.getByLabelText(/假设扫描时间|Assumed Scan Time/), { target: { value: "2026-10-18T10:20:30.500" } });
      expect(screen.getByText(/扫描时间格式不正确|Invalid scan time format/)).toBeInTheDocument();
      expect(runButton()).toBeDisabled();
    });

    it("rejects C1 control characters and negative node IDs like the backend", async () => {
      openDraft();
      fireEvent.change(screen.getByLabelText(ERROR_CODE), { target: { value: "XR\u0085EXEC" } });
      expect(screen.getByText(/不含控制字符|without control characters/)).toBeInTheDocument();
      expect(runButton()).toBeDisabled();

      fireEvent.change(screen.getByLabelText(ERROR_CODE), { target: { value: "XR-EXEC-7" } });
      fireEvent.change(screen.getByLabelText(/节点 ID|Node ID/), { target: { value: "-1" } });
      expect(screen.getByText(/节点 ID 必须为非负整数|Node ID must be a non-negative integer/)).toBeInTheDocument();
      expect(runButton()).toBeDisabled();

      fireEvent.change(screen.getByLabelText(/节点 ID|Node ID/), { target: { value: "5" } });
      expect(runButton()).not.toBeDisabled();
      fireEvent.click(runButton());
      await waitFor(() => expect(calls).toHaveLength(1));
    });

    it("blocks a draft whose name exceeds 100 UTF-8 bytes and explains why", () => {
      const longName = "名".repeat(34); // 102 UTF-8 bytes
      openDraft({ policy: { ...sampleDraftPolicy, name: longName } });
      expect(runButton()).toBeDisabled();
      expect(screen.getByText(/请先修正草稿策略字段|Fix the draft policy fields/)).toBeInTheDocument();
    });

    it("accepts the backend delay bound and rejects one second above it", () => {
      const withDelay = (delaySeconds: number) => ({
        ...sampleDraftPolicy,
        levels: [sampleDraftPolicy.levels[0], { ...sampleDraftPolicy.levels[1], delaySeconds }],
      });
      const { rerender } = openDraft({ policy: withDelay(9_223_372_036) });
      expect(runButton()).not.toBeDisabled();
      rerender(draftUi({ policy: withDelay(9_223_372_037) }));
      expect(runButton()).toBeDisabled();
    });

    it("keeps scenario inputs but discards the result as soon as the draft changes", async () => {
      const { rerender } = openDraft();
      fireEvent.change(screen.getByLabelText(/节点 ID|Node ID/), { target: { value: "5" } });
      await runToReady();

      const edited = { ...sampleDraftPolicy, levels: [sampleDraftPolicy.levels[0], { ...sampleDraftPolicy.levels[1], delaySeconds: 600 }] };
      rerender(draftUi({ policy: edited }));
      expect((screen.getByLabelText(ERROR_CODE) as HTMLInputElement).value).toBe("XR-TASK-001");
      expect((screen.getByLabelText(/节点 ID|Node ID/) as HTMLInputElement).value).toBe("5");
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(statusRegion()).toHaveTextContent(IDLE);
    });

    it("cancels an in-flight request immediately when saving starts and never restores it", async () => {
      honorAbort = false;
      const { rerender } = openDraft();
      fireEvent.click(runButton());
      await flush();
      expect(calls).toHaveLength(1);

      rerender(draftUi({ active: false, disabled: true }));
      expect(calls[0].signal.aborted).toBe(true);
      expect(statusRegion()).toHaveTextContent(IDLE);

      await respond(0, envelope(validDraftResponse));
      rerender(draftUi());
      await flush();
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(statusRegion()).toHaveTextContent(IDLE);
    });

    it("clears a ready result when enablement flips, without requesting again", async () => {
      const { rerender } = openDraft();
      await runToReady();
      rerender(draftUi({ disabled: true }));
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(runButton()).toBeDisabled();
      rerender(draftUi({ disabled: false }));
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(calls).toHaveLength(1);
    });

    it("refuses previews while channels failed to load", () => {
      openDraft({ integrationsFailed: true });
      expect(runButton()).toBeDisabled();
      expect(screen.getByText(/渠道列表加载成功前无法预演|Preview is unavailable until the channel list loads/)).toBeInTheDocument();
    });

    it("issues one request for same-tick double clicks", async () => {
      openDraft();
      const button = runButton();
      fireEvent.click(button);
      fireEvent.click(button);
      await flush();
      expect(calls).toHaveLength(1);
      expect(runButton()).toHaveAttribute("aria-busy", "true");
    });

    it("expires on the monotonic clock after subtracting the round trip", async () => {
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
      openDraft();
      fireEvent.click(runButton());
      await flush();
      monotonicNow = 10_000; // 10s round trip
      await respond(0, envelope(validDraftResponse));
      expect(screen.getByText("Ops Webhook")).toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(49_999);
      });
      expect(screen.getByText("Ops Webhook")).toBeInTheDocument();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      expect(statusRegion()).toHaveTextContent(EXPIRED);
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(calls).toHaveLength(1);
    });

    it("shows expired immediately when the round trip consumed the whole TTL", async () => {
      openDraft();
      fireEvent.click(runButton());
      await flush();
      monotonicNow = 60_000;
      await respond(0, envelope(validDraftResponse));
      expect(statusRegion()).toHaveTextContent(EXPIRED);
    });

    it("re-checks identity when the TTL timer fires instead of trusting the attempt alone", async () => {
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
      const { rerender } = openDraft();
      await runToReady();

      rememberAuthIdentity("tok-other", "admin");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      rememberAuthIdentity("tok-admin", "admin");
      rerender(draftUi());
      expect(statusRegion()).toHaveTextContent(IDLE);
      expect(statusRegion()).not.toHaveTextContent(EXPIRED);
    });

    it("shows a retryable inline failure and recovers on retry", async () => {
      openDraft();
      fireEvent.click(runButton());
      await flush();
      await respond(0, failure(500));
      expect(statusRegion()).toHaveTextContent(FAILED);

      fireEvent.click(screen.getByRole("button", { name: /^(重试|Retry)$/ }));
      await flush();
      expect(calls).toHaveLength(2);
      expect(statusRegion()).not.toHaveTextContent(FAILED);
      await respond(1, envelope(validDraftResponse));
      expect(screen.getByText("Ops Webhook")).toBeInTheDocument();
    });

    it("treats a malformed payload as a failure instead of a partial preview", async () => {
      openDraft();
      fireEvent.click(runButton());
      await flush();
      await respond(0, envelope({ ...validDraftResponse, mode: "alert" }));
      expect(statusRegion()).toHaveTextContent(FAILED);
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
    });

    it("ignores late success and failure after a generation change (A→B→A) even when abort is ignored", async () => {
      honorAbort = false;
      openDraft();
      fireEvent.click(runButton());
      await flush();
      fireEvent.click(screen.getByRole("button", { name: DRAFT_TOGGLE }));
      fireEvent.click(screen.getByRole("button", { name: DRAFT_TOGGLE }));
      fireEvent.change(screen.getByLabelText(ERROR_CODE), { target: { value: "XR-TASK-001" } });
      fireEvent.click(runButton());
      await flush();
      expect(calls).toHaveLength(2);

      act(() => {
        bumpAuthSessionGeneration(); // A → B
      });
      expect(calls.every((call) => call.signal.aborted)).toBe(true);
      act(() => {
        bumpAuthSessionGeneration(); // B → A, same token and role
      });

      await respond(1, envelope(validDraftResponse));
      calls[0].reject(new Error("late network failure"));
      await flush();

      // The whole draft session was discarded; nothing from the old generation reappears.
      expect(screen.queryByLabelText(ERROR_CODE)).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: DRAFT_TOGGLE }));
      expect((screen.getByLabelText(ERROR_CODE) as HTMLInputElement).value).toBe("");
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(screen.queryByText(FAILED)).not.toBeInTheDocument();
    });

    it("discards the draft session on token A→B→A", async () => {
      const { rerender } = openDraft();
      await runToReady();
      authState.token = "tok-b";
      rememberAuthIdentity("tok-b", "admin");
      rerender(draftUi({ token: "tok-b" }));
      authState.token = "tok-admin";
      rememberAuthIdentity("tok-admin", "admin");
      rerender(draftUi());
      expect(screen.queryByLabelText(ERROR_CODE)).not.toBeInTheDocument();
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
    });

    it("hides results and explains when the role is no longer admin", async () => {
      const { rerender } = openDraft();
      await runToReady();
      authState.role = "operator";
      rememberAuthIdentity("tok-admin", "operator");
      rerender(draftUi());
      fireEvent.click(screen.getByRole("button", { name: DRAFT_TOGGLE }));
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(runButton()).toBeDisabled();
      expect(statusRegion()).toHaveTextContent(/需要管理员权限|admin required/);
    });

    it("retires in-flight work at the transition barrier and does not restore it after release", async () => {
      honorAbort = false;
      openDraft();
      fireEvent.click(runButton());
      await flush();

      let barrier = 0;
      act(() => {
        barrier = beginAuthTransitionBarrier();
      });
      expect(calls[0].signal.aborted).toBe(true);
      expect(runButton()).toBeDisabled();
      await respond(0, envelope(validDraftResponse));
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();

      act(() => {
        releaseAuthTransitionBarrier(barrier);
      });
      await flush();
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(statusRegion()).toHaveTextContent(IDLE);
      expect(runButton()).not.toBeDisabled();
    });

    it("expires a shown result on window blur and on hidden visibility", async () => {
      openDraft();
      await runToReady();
      act(() => {
        window.dispatchEvent(new Event("blur"));
      });
      expect(statusRegion()).toHaveTextContent(EXPIRED);

      await runToReady();
      Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
      act(() => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      expect(statusRegion()).toHaveTextContent(EXPIRED);
    });

    it("collapsing and reopening the drawer discards the result", async () => {
      openDraft();
      await runToReady();
      fireEvent.click(screen.getByRole("button", { name: DRAFT_TOGGLE }));
      fireEvent.click(screen.getByRole("button", { name: DRAFT_TOGGLE }));
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
      expect(statusRegion()).toHaveTextContent(IDLE);
    });

    it("aborts and ignores a request that settles after unmount", async () => {
      honorAbort = false;
      const { unmount } = openDraft();
      fireEvent.click(runButton());
      await flush();
      unmount();
      expect(calls[0].signal.aborted).toBe(true);
      await respond(0, envelope(validDraftResponse));
      expect(screen.queryByText("Ops Webhook")).not.toBeInTheDocument();
    });
  });

  describe("AlertEscalationPreview", () => {
    const alertUi = (props: { alertId?: number | null; revisionKey?: string; token?: string } = {}) => (
      <AlertEscalationPreview alertId={42} token="tok-admin" active revisionKey="rev-1" {...props} />
    );
    const diagnoseButton = () => screen.getByRole("button", { name: DIAGNOSE });

    it("renders nothing and never requests for non-admin users", async () => {
      authState.role = "operator";
      const { container } = render(alertUi());
      await flush();
      expect(container).toBeEmptyDOMElement();
      expect(calls).toHaveLength(0);
    });

    it("renders nothing without a token", () => {
      const { container } = render(alertUi({ token: "" }));
      expect(container).toBeEmptyDOMElement();
    });

    it("disables diagnosis when the alert ID could not be parsed", () => {
      render(alertUi({ alertId: null }));
      expect(diagnoseButton()).toBeDisabled();
      expect(screen.getByText(/无效的告警 ID，无法诊断|Invalid alert ID, cannot diagnose/)).toBeInTheDocument();
    });

    it("requests only on click and renders source, levels, warnings and all delivery counts", async () => {
      render(alertUi());
      await flush();
      expect(calls).toHaveLength(0);

      fireEvent.click(diagnoseButton());
      await waitFor(() => expect(calls).toHaveLength(1));
      expect(calls[0].url).toContain("/api/v1/alerts/42/escalation-preview");
      expect(calls[0].init.method).toBe("GET");
      expect(calls[0].init.body).toBeUndefined();

      await respond(0, envelope(validAlertResponse));
      const region = statusRegion();
      expect(region).toHaveTextContent("Task Policy");
      expect(region).toHaveTextContent("#88");
      expect(region).toHaveTextContent("#9");
      expect(region).toHaveTextContent(/策略在历史升级事件后已发生变更|Policy changed since historical escalation events/);
      expect(region).toHaveTextContent(/2 已发送|2 sent/);
      expect(region).toHaveTextContent(/3 未知|3 unknown/);
    });

    it("explains not_open results and keeps delivery counts", async () => {
      render(alertUi());
      fireEvent.click(diagnoseButton());
      await flush();
      await respond(
        0,
        envelope({
          ...validAlertResponse,
          status: "not_open",
          alert: { ...validAlertResponse.alert, status: "resolved" },
          next_level_index: null,
          next_due_at: null,
          next_due_now: null,
          steps: [],
        })
      );
      const region = statusRegion();
      expect(region).toHaveTextContent(/告警已确认或解决，不投射新级别|Alert is acknowledged or resolved/);
      expect(region).toHaveTextContent(/确认\/解决不会撤销已有投递意图|Acknowledgment\/resolution does not revoke/);
      expect(region).toHaveTextContent(/2 已发送|2 sent/);
    });

    it("renders unsupported_state with alert=null as an explanation, not as an empty policy", async () => {
      render(alertUi());
      fireEvent.click(diagnoseButton());
      await flush();
      await respond(
        0,
        envelope({
          ...validAlertResponse,
          status: "unsupported_state",
          policy_source: "none",
          source_resource_id: null,
          policy: null,
          alert: null,
          next_level_index: null,
          next_due_at: null,
          next_due_now: null,
          delivery_decision: null,
          delivery_counts: null,
          warnings: ["unsupported_alert_state", "node_missing"],
          steps: [],
        })
      );
      const region = statusRegion();
      expect(region).toHaveTextContent(/当前数据状态不支持安全预演|Current data state does not support safe preview/);
      expect(region).toHaveTextContent(/告警状态不支持安全预演|Alert state does not support safe preview/);
      expect(region).not.toHaveTextContent(/未绑定策略|No Policy Bound/);
    });

    it("shows an inline unavailable state with retry when the server has no preview endpoint", async () => {
      const previousLanguage = i18n.language;
      await act(async () => {
        await i18n.changeLanguage("en");
      });
      try {
        render(alertUi({ alertId: 123 }));
        await flush();
        expect(calls).toHaveLength(0);

        fireEvent.click(diagnoseButton());
        await waitFor(() => expect(calls).toHaveLength(1));
        expect(calls[0].url).toMatch(/\/api\/v1\/alerts\/123\/escalation-preview$/);
        expect(calls[0].init.method).toBe("GET");
        await respond(
          0,
          new Response(JSON.stringify({ code: 404, message: "not found", data: null }), {
            status: 404,
            headers: { "Content-Type": "application/json" },
          })
        );

        const region = statusRegion();
        expect(region).toHaveTextContent("Escalation preview unavailable");
        expect(region).not.toHaveTextContent("Would Enqueue");
        expect(screen.queryByRole("list", { name: "Projected escalation levels" })).not.toBeInTheDocument();
        await flush();
        expect(calls).toHaveLength(1);

        fireEvent.click(screen.getByRole("button", { name: "Retry" }));
        await waitFor(() => expect(calls).toHaveLength(2));
        expect(calls[1].url).toMatch(/\/api\/v1\/alerts\/123\/escalation-preview$/);
        await flush();
        expect(calls).toHaveLength(2);
      } finally {
        await act(async () => {
          await i18n.changeLanguage(previousLanguage);
        });
      }
    });

    it("rejects a response for a different alert", async () => {
      render(alertUi());
      fireEvent.click(diagnoseButton());
      await flush();
      await respond(0, envelope({ ...validAlertResponse, requested_alert_id: 43 }));
      expect(statusRegion()).toHaveTextContent(FAILED);
    });

    it("discards the result when the alert's public fields change", async () => {
      const { rerender } = render(alertUi());
      fireEvent.click(diagnoseButton());
      await flush();
      await respond(0, envelope(validAlertResponse));
      expect(statusRegion()).toHaveTextContent("Task Policy");
      rerender(alertUi({ revisionKey: "rev-2" }));
      expect(statusRegion()).not.toHaveTextContent("Task Policy");
      expect(calls).toHaveLength(1);
    });
  });
});
