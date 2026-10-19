import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  MAX_PREVIEW_DELAY_SECONDS,
  getUtf8ByteLength,
  hasControlCharacter,
  isUtcTimestamp,
  previewAlertEscalation,
  previewEscalationDraft,
  type DraftEscalationScenario,
} from "./escalation-preview";
import type { EscalationPolicyInput } from "./escalation";

const mockRequest = vi.fn();

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: (...args: unknown[]) => mockRequest(...args),
  };
});

type Wire = Record<string, unknown>;

const samplePolicyInput: EscalationPolicyInput = {
  name: "Production Escalation",
  description: "Alert policy for core services",
  minSeverity: "warning",
  enabled: true,
  levels: [
    { delaySeconds: 0, integrationIds: [1, 2], severityOverride: "critical", tags: ["ops"] },
    { delaySeconds: 300, integrationIds: [3], severityOverride: "", tags: ["secondary"] },
  ],
};

const sampleScenario: DraftEscalationScenario = {
  nodeId: 10,
  errorCode: "XR-TASK-001",
  severity: "warning",
  tags: ["backend"],
  triggeredAt: "2026-10-18T10:00:00Z",
  evaluationAt: "2026-10-18T10:00:00Z",
};

function draftWire(): Wire {
  return {
    mode: "draft",
    basis: "current_snapshot",
    generated_at: "2026-10-18T10:00:00.123456789Z",
    expires_at: "2026-10-18T10:01:00.123456789Z",
    evaluation_at: "2026-10-18T10:00:00Z",
    tick_seconds: 30,
    requested_alert_id: null,
    status: "projected",
    policy_source: "draft",
    source_resource_id: null,
    policy: { id: null, name: "Production Escalation", min_severity: "warning", enabled: true, updated_at: null },
    alert: {
      id: null,
      node_id: 10,
      severity: "warning",
      status: "open",
      triggered_at: "2026-10-18T10:00:00Z",
      last_level_fired: -1,
      tags: ["backend"],
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
        status: "silenced",
        severity_before: "warning",
        severity_after: "critical",
        tags_after: ["backend", "ops"],
        matched_silence_ids: [101, 205],
        channels: [
          { id: 1, name: "Webhook A", type: "webhook", state: "enabled" },
          { id: 2, name: "Slack B", type: "slack", state: "disabled" },
        ],
      },
      {
        level_index: 1,
        delay_seconds: 300,
        due_at: "2026-10-18T10:05:00Z",
        projected_at: "2026-10-18T10:05:00Z",
        status: "no_enabled_channels",
        severity_before: "critical",
        severity_after: "critical",
        tags_after: ["backend", "ops", "secondary"],
        matched_silence_ids: [],
        channels: [{ id: 3, name: null, type: null, state: "missing" }],
      },
    ],
  };
}

function alertWire(): Wire {
  return {
    mode: "alert",
    basis: "current_snapshot",
    generated_at: "2026-10-18T10:00:00Z",
    expires_at: "2026-10-18T10:01:00Z",
    evaluation_at: "2026-10-18T10:00:00Z",
    tick_seconds: 30,
    requested_alert_id: 42,
    status: "projected",
    policy_source: "task",
    source_resource_id: 100,
    policy: { id: 5, name: "Task Escalation Policy", min_severity: "warning", enabled: true, updated_at: "2026-10-17T08:00:00Z" },
    alert: {
      id: 42,
      node_id: 12,
      severity: "warning",
      status: "open",
      triggered_at: "2026-10-18T09:30:00Z",
      last_level_fired: 0,
      tags: ["prod"],
    },
    next_level_index: 1,
    next_due_at: "2026-10-18T09:35:00Z",
    next_due_now: true,
    delivery_decision: "escalated",
    delivery_counts: { pending: 0, sending: 1, retrying: 0, sent: 2, failed: 1, unknown: 0 },
    warnings: ["node_missing"],
    steps: [
      {
        level_index: 1,
        delay_seconds: 300,
        due_at: "2026-10-18T09:35:00Z",
        projected_at: "2026-10-18T10:00:00Z",
        status: "would_enqueue",
        severity_before: "warning",
        severity_after: "info",
        tags_after: ["prod"],
        matched_silence_ids: [],
        channels: [{ id: 1, name: "Webhook", type: "webhook", state: "enabled" }],
      },
      {
        level_index: 2,
        delay_seconds: 900,
        due_at: "2026-10-18T09:45:00Z",
        projected_at: null,
        status: "blocked_minimum",
        severity_before: "info",
        severity_after: "info",
        tags_after: ["prod"],
        matched_silence_ids: [],
        channels: [{ id: 1, name: "Webhook", type: "webhook", state: "enabled" }],
      },
      {
        level_index: 3,
        delay_seconds: 1800,
        due_at: "2026-10-18T10:00:00Z",
        projected_at: null,
        status: "blocked_prior",
        severity_before: "info",
        severity_after: "info",
        tags_after: ["prod"],
        matched_silence_ids: [],
        channels: [{ id: 1, name: "Webhook", type: "webhook", state: "enabled" }],
      },
    ],
  };
}

function unsupportedWire(): Wire {
  return {
    ...alertWire(),
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
    warnings: ["unsupported_alert_state"],
    steps: [],
  };
}

function step(wire: Wire, index: number): Wire {
  return (wire.steps as Wire[])[index];
}

async function decodeAlert(wire: Wire) {
  mockRequest.mockResolvedValue(wire);
  return previewAlertEscalation("tok-admin", 42);
}

async function decodeDraft(wire: Wire) {
  mockRequest.mockResolvedValue(wire);
  return previewEscalationDraft("tok-1", { policy: samplePolicyInput, scenario: sampleScenario });
}

describe("escalation-preview API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("primitives", () => {
    it("accepts only server UTC timestamps with real calendar fields", () => {
      expect(isUtcTimestamp("2026-10-18T10:00:00Z")).toBe(true);
      expect(isUtcTimestamp("2026-10-18T10:00:00.123456789Z")).toBe(true);
      expect(isUtcTimestamp("2024-02-29T12:00:00Z")).toBe(true);
      expect(isUtcTimestamp("0001-01-01T00:00:00Z")).toBe(true);
      expect(isUtcTimestamp("9999-12-31T23:59:59.999999999Z")).toBe(true);

      expect(isUtcTimestamp("2026-10-18T10:00:00+00:00")).toBe(false);
      expect(isUtcTimestamp("2026-10-18T10:00:00+08:00")).toBe(false);
      expect(isUtcTimestamp("2026-12-31T23:59:60Z")).toBe(false);
      expect(isUtcTimestamp("2026-02-29T10:00:00Z")).toBe(false);
      expect(isUtcTimestamp("2026-04-31T10:00:00Z")).toBe(false);
      expect(isUtcTimestamp("0000-01-01T00:00:00Z")).toBe(false);
      expect(isUtcTimestamp("2026-10-18T10:00:00.1234567890Z")).toBe(false);
      expect(isUtcTimestamp("2026-10-18 10:00:00Z")).toBe(false);
    });

    it("measures UTF-8 bytes and matches Go unicode.IsControl", () => {
      expect(getUtf8ByteLength("中文")).toBe(6);
      expect(getUtf8ByteLength("🔥")).toBe(4);
      for (const control of ["\u0000", "\u001f", "\u007f", "\u0085", "\u009f"]) {
        expect(hasControlCharacter(`A${control}B`)).toBe(true);
      }
      expect(hasControlCharacter("XR-EXEC-7\u00a0")).toBe(false);
    });
  });

  describe("previewEscalationDraft request", () => {
    it("serializes the full draft and scenario and maps the response", async () => {
      const result = await decodeDraft(draftWire());

      const [path, options] = mockRequest.mock.calls[0] as [string, { token?: string; method?: string; body?: unknown }];
      expect(path).toBe("/escalation-policies/preview");
      expect(options.token).toBe("tok-1");
      expect(options.method).toBe("POST");
      expect(options.body).toEqual({
        policy: {
          name: "Production Escalation",
          description: "Alert policy for core services",
          min_severity: "warning",
          enabled: true,
          levels: [
            { delay_seconds: 0, integration_ids: [1, 2], severity_override: "critical", tags: ["ops"] },
            { delay_seconds: 300, integration_ids: [3], severity_override: "", tags: ["secondary"] },
          ],
        },
        scenario: {
          node_id: 10,
          error_code: "XR-TASK-001",
          severity: "warning",
          tags: ["backend"],
          triggered_at: "2026-10-18T10:00:00Z",
          evaluation_at: "2026-10-18T10:00:00Z",
        },
      });

      expect(result.requestedAlertId).toBeNull();
      expect(result.steps.map((item) => item.status)).toEqual(["silenced", "no_enabled_channels"]);
      expect(result.steps[0].matchedSilenceIds).toEqual([101, 205]);
      expect(result.steps[1].channels[0]).toEqual({ id: 3, name: null, type: null, state: "missing" });
    });

    it.each([
      ["error code above 128 bytes", { scenario: { errorCode: "A".repeat(129) } }],
      ["C1 control in error code", { scenario: { errorCode: "XR\u0085" } }],
      ["tag above 32 bytes", { scenario: { tags: ["中".repeat(11)] } }],
      ["more than 10 scenario tags", { scenario: { tags: Array.from({ length: 11 }, (_, i) => `t${i}`) } }],
      ["offset timestamp", { scenario: { triggeredAt: "2026-10-18T18:00:00+08:00" } }],
      ["negative node", { scenario: { nodeId: -1 } }],
      ["delay above the Go duration bound", { delay: MAX_PREVIEW_DELAY_SECONDS + 1 }],
      ["unknown severity override", { override: "fatal" }],
    ])("rejects %s before requesting", async (_label, change) => {
      const scenario = { ...sampleScenario, ...("scenario" in change ? change.scenario : {}) } as DraftEscalationScenario;
      const levels = samplePolicyInput.levels.map((level, index) =>
        index === 1
          ? {
              ...level,
              delaySeconds: "delay" in change ? change.delay : level.delaySeconds,
              severityOverride: ("override" in change ? change.override : level.severityOverride) as "",
            }
          : level
      );
      await expect(previewEscalationDraft("tok-1", { policy: { ...samplePolicyInput, levels }, scenario })).rejects.toThrow(/Invalid draft/);
      expect(mockRequest).not.toHaveBeenCalled();
    });

    it("accepts the exact backend delay bound", async () => {
      mockRequest.mockResolvedValue(draftWire());
      const levels = [samplePolicyInput.levels[0], { ...samplePolicyInput.levels[1], delaySeconds: MAX_PREVIEW_DELAY_SECONDS }];
      await expect(
        previewEscalationDraft("tok-1", { policy: { ...samplePolicyInput, levels }, scenario: sampleScenario })
      ).resolves.toBeTruthy();
    });
  });

  describe("response contract", () => {
    it("maps alert diagnosis including blocked levels", async () => {
      const result = await decodeAlert(alertWire());
      expect(mockRequest).toHaveBeenCalledWith("/alerts/42/escalation-preview", { method: "GET", token: "tok-admin", signal: undefined });
      expect(result.requestedAlertId).toBe(42);
      expect(result.sourceResourceId).toBe(100);
      expect(result.deliveryCounts).toEqual({ pending: 0, sending: 1, retrying: 0, sent: 2, failed: 1, unknown: 0 });
      expect(result.steps.map((item) => [item.status, item.projectedAt])).toEqual([
        ["would_enqueue", "2026-10-18T10:00:00Z"],
        ["blocked_minimum", null],
        ["blocked_prior", null],
      ]);
    });

    it("accepts unsupported_state with alert=null when requested_alert_id matches", async () => {
      const result = await decodeAlert(unsupportedWire());
      expect(result).toMatchObject({ status: "unsupported_state", requestedAlertId: 42, alert: null, policy: null, steps: [] });
    });

    it("does not cap persisted or projected tag unions", async () => {
      const wire = alertWire();
      const many = Array.from({ length: 200 }, (_, i) => `legacy-tag-${i}-${"x".repeat(40)}`);
      (wire.alert as Wire).tags = many;
      step(wire, 0).tags_after = many;
      const result = await decodeAlert(wire);
      expect(result.alert?.tags).toHaveLength(200);
    });

    it("accepts year 1 timestamps without remapping", async () => {
      const wire = alertWire();
      (wire.policy as Wire).updated_at = "0001-01-01T00:00:00Z";
      await expect(decodeAlert(wire)).resolves.toBeTruthy();
    });

    it.each<[string, (wire: Wire) => void]>([
      ["requested_alert_id mismatch", (w) => { w.requested_alert_id = 43; }],
      ["alert.id mismatch", (w) => { (w.alert as Wire).id = 999; }],
      ["unsafe integer ID", (w) => { w.source_resource_id = 2 ** 53; }],
      ["expires_at not generated_at+60s", (w) => { w.expires_at = "2026-10-18T10:00:59Z"; }],
      ["expires_at fraction drift", (w) => { w.expires_at = "2026-10-18T10:01:00.5Z"; }],
      ["offset timestamp", (w) => { w.generated_at = "2026-10-18T10:00:00+00:00"; }],
      ["leap second", (w) => { step(w, 0).projected_at = "2026-10-18T10:00:60Z"; }],
      ["alert evaluation not at generated_at", (w) => { w.evaluation_at = "2026-10-18T10:00:30Z"; }],
      ["unknown warning", (w) => { w.warnings = ["mystery"]; }],
      ["duplicate warning", (w) => { w.warnings = ["node_missing", "node_missing"]; }],
      ["unsupported-only warning on a projected result", (w) => { w.warnings = ["event_cursor_conflict"]; }],
      ["unknown alert severity", (w) => { (w.alert as Wire).severity = "fatal"; }],
      ["unknown alert status", (w) => { (w.alert as Wire).status = "closed"; }],
      ["unknown step severity", (w) => { step(w, 0).severity_after = "emergency"; }],
      ["missing alert on a supported result", (w) => { w.alert = null; }],
      ["projected without a policy", (w) => { w.policy = null; w.policy_source = "none"; w.source_resource_id = null; }],
      ["source without resource ID", (w) => { w.source_resource_id = null; }],
      ["projected without next_due_at", (w) => { w.next_due_at = null; }],
      ["next_level_index not following last_level_fired", (w) => { w.next_level_index = 2; }],
      ["next_due_now disagreeing with time", (w) => { w.next_due_now = false; }],
      ["non-projected status with steps", (w) => { w.status = "complete"; }],
      ["not_open while alert is open", (w) => { w.status = "not_open"; w.steps = []; w.next_level_index = null; w.next_due_at = null; w.next_due_now = null; }],
      ["disabled while policy is enabled", (w) => { w.status = "disabled"; w.steps = []; w.next_level_index = null; w.next_due_at = null; w.next_due_now = null; }],
      ["blocked step with projected_at", (w) => { step(w, 1).projected_at = "2026-10-18T10:00:30Z"; }],
      ["unblocked step without projected_at", (w) => { step(w, 0).projected_at = null; }],
      ["blocked_prior before blocked_minimum", (w) => { step(w, 1).status = "blocked_prior"; }],
      ["projected_at before due_at", (w) => { w.evaluation_at = "2026-10-18T09:00:00Z"; w.generated_at = "2026-10-18T09:00:00Z"; w.expires_at = "2026-10-18T09:01:00Z"; w.next_due_now = false; step(w, 0).projected_at = "2026-10-18T09:30:00Z"; }],
      ["would_enqueue without an enabled channel", (w) => { step(w, 0).channels = [{ id: 1, name: "Webhook", type: "webhook", state: "disabled" }]; }],
      ["silence IDs without silenced status", (w) => { step(w, 0).matched_silence_ids = [5]; }],
      ["non-ascending silence IDs", (w) => { step(w, 0).status = "silenced"; step(w, 0).matched_silence_ids = [9, 5]; }],
      ["more than 1000 silence IDs", (w) => { step(w, 0).status = "silenced"; step(w, 0).matched_silence_ids = Array.from({ length: 1001 }, (_, i) => i + 1); }],
      ["more than 100 channels", (w) => { step(w, 0).channels = Array.from({ length: 101 }, (_, i) => ({ id: i + 1, name: "c", type: "webhook", state: "enabled" })); }],
      ["duplicate channel IDs", (w) => { step(w, 0).channels = [{ id: 1, name: "a", type: "webhook", state: "enabled" }, { id: 1, name: "a", type: "webhook", state: "enabled" }]; }],
      ["missing channel with a name", (w) => { step(w, 0).channels = [{ id: 1, name: "ghost", type: null, state: "missing" }, { id: 2, name: "a", type: "webhook", state: "enabled" }]; }],
      ["non-string tag", (w) => { step(w, 0).tags_after = ["ok", 7]; }],
      ["more than five steps", (w) => { w.steps = Array.from({ length: 6 }, () => step(w, 0)); }],
      ["delay above the Go duration bound", (w) => { step(w, 2).delay_seconds = MAX_PREVIEW_DELAY_SECONDS + 1; }],
      ["unsupported_state with a partial policy", (w) => { Object.assign(w, unsupportedWire(), { policy: alertWire().policy }); }],
      ["unsupported_state with delivery counts", (w) => { Object.assign(w, unsupportedWire(), { delivery_counts: alertWire().delivery_counts }); }],
      ["unsupported_state with a policy source", (w) => { Object.assign(w, unsupportedWire(), { policy_source: "task" }); }],
      ["unsupported_state without warnings", (w) => { Object.assign(w, unsupportedWire(), { warnings: [] }); }],
      ["unsupported_state with requested_alert_id mismatch", (w) => { Object.assign(w, unsupportedWire(), { requested_alert_id: 7 }); }],
    ])("rejects %s", async (_label, mutate) => {
      const wire = alertWire();
      mutate(wire);
      await expect(decodeAlert(wire)).rejects.toThrow(/Invalid escalation preview response/);
    });

    it.each<[string, (wire: Wire) => void]>([
      ["mode mismatch", (w) => { w.mode = "alert"; }],
      ["non-null requested_alert_id", (w) => { w.requested_alert_id = 1; }],
      ["policy id", (w) => { (w.policy as Wire).id = 3; }],
      ["delivery counts", (w) => { w.delivery_counts = { pending: 0, sending: 0, retrying: 0, sent: 0, failed: 0, unknown: 0 }; }],
      ["alert-only warning", (w) => { w.warnings = ["node_missing"]; }],
      ["non-draft source", (w) => { w.policy_source = "task"; w.source_resource_id = 1; }],
      ["already fired cursor", (w) => { (w.alert as Wire).last_level_fired = 0; }],
    ])("rejects a draft response with %s", async (_label, mutate) => {
      const wire = draftWire();
      mutate(wire);
      await expect(decodeDraft(wire)).rejects.toThrow(/Invalid escalation preview response/);
    });

    it("rejects non-positive alert IDs before requesting", async () => {
      await expect(previewAlertEscalation("tok-admin", -1)).rejects.toThrow(/Invalid alertId/);
      expect(mockRequest).not.toHaveBeenCalled();
    });

    it("passes the AbortSignal through", async () => {
      mockRequest.mockResolvedValue(alertWire());
      const controller = new AbortController();
      await previewAlertEscalation("tok-admin", 42, { signal: controller.signal });
      expect(mockRequest).toHaveBeenCalledWith("/alerts/42/escalation-preview", expect.objectContaining({ signal: controller.signal }));
    });
  });
});
