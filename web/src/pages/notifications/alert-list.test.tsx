import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n, { setLanguage } from "@/i18n";
import {
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  rememberAuthIdentity,
} from "@/lib/api/core";
import { createAlertsApi } from "@/lib/api/alerts-api";
import { AlertList, type AlertListProps } from "./alert-list";
import type { AlertRecord } from "@/types/domain";

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
  resolve: (response: Response) => void;
  reject: (reason: unknown) => void;
};

let calls: PendingFetch[] = [];

// Sibling panels (history timeline, anomaly context) also fetch on mount; those stay pending.
const fetchMock = vi.fn(
  (input: RequestInfo | URL, init: RequestInit = {}) =>
    new Promise<Response>((resolve, reject) => {
      calls.push({ url: String(input), init, resolve, reject });
    })
);

const DIAGNOSE = /^(诊断升级（只读）|Diagnose Escalation \(Read-only\)|重新诊断|Diagnose Again)$/;
const DIAGNOSE_IDLE = /点击诊断以只读分析|Click diagnose to analyze/;

function envelope(data: unknown): Response {
  return new Response(JSON.stringify({ code: 200, message: "ok", data }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

const previewCalls = () => calls.filter((call) => call.url.includes("/escalation-preview"));
const timelineCalls = () => calls.filter((call) => call.url.includes("/escalation-events"));
const configNavs = () => screen.queryAllByRole("navigation", { name: /^(告警配置|Alert configuration)$/ });
const CONFIG_HINT = /配置后可返回此告警|return to this alert/i;

const alertRow = {
  id: 123,
  node_id: 1,
  node_name: "node-alpha",
  policy_name: "Default Escalation",
  severity: "critical",
  status: "open",
  error_code: "XR-TASK-001",
  message: "Task execution timeout",
  retryable: true,
  triggered_at: "2026-10-18T10:00:00Z",
};

const validAlertPreviewResponse = {
  mode: "alert",
  basis: "current_snapshot",
  generated_at: "2026-10-18T10:00:00Z",
  expires_at: "2026-10-18T10:01:00Z",
  evaluation_at: "2026-10-18T10:00:00Z",
  tick_seconds: 30,
  requested_alert_id: 123,
  status: "projected",
  policy_source: "task",
  source_resource_id: 5,
  policy: { id: 10, name: "Production Escalation", min_severity: "warning", enabled: true, updated_at: "2026-10-17T00:00:00Z" },
  alert: {
    id: 123,
    node_id: 1,
    severity: "critical",
    status: "open",
    triggered_at: "2026-10-18T10:00:00Z",
    last_level_fired: -1,
    tags: [],
  },
  next_level_index: 0,
  next_due_at: "2026-10-18T10:00:00Z",
  next_due_now: true,
  delivery_decision: "escalated",
  delivery_counts: { pending: 1, sending: 0, retrying: 0, sent: 0, failed: 0, unknown: 0 },
  warnings: [],
  steps: [
    {
      level_index: 0,
      delay_seconds: 0,
      due_at: "2026-10-18T10:00:00Z",
      projected_at: "2026-10-18T10:00:00Z",
      status: "would_enqueue",
      severity_before: "critical",
      severity_after: "critical",
      tags_after: [],
      matched_silence_ids: [],
      channels: [{ id: 1, name: "Webhook Primary", type: "webhook", state: "enabled" }],
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

/** Produces the AlertRecord through the production mapper, which yields the "alert-123" ID form. */
async function loadMappedAlert(): Promise<AlertRecord> {
  const pending = createAlertsApi().getAlert("tok-admin", "alert-123");
  await flush();
  const call = calls.find((entry) => entry.url.endsWith("/api/v1/alerts/123"));
  call!.resolve(envelope(alertRow));
  const alert = await pending;
  calls = [];
  return alert;
}

function listUi(alerts: AlertRecord[], overrides: Partial<AlertListProps> = {}) {
  const props: AlertListProps = {
    token: "tok-admin",
    alerts,
    highlightedAlertId: null,
    highlightRef: vi.fn(),
    viewMode: "list",
    sortBy: "triggered_at",
    sortOrder: "desc",
    onToggleSort: vi.fn(),
    deliveryOpenAlertId: alerts[0]?.id ?? null,
    deliveryLoadingAlertId: null,
    deliveryMap: Object.fromEntries(alerts.map((alert) => [alert.id, []])),
    retryingDeliveryKey: null,
    retryingAllAlertId: null,
    integrationNameMap: new Map(),
    selectedAlertIds: [],
    bulkResolving: false,
    canWriteAlerts: true,
    canTriggerTasks: true,
    canRetryDelivery: true,
    canManageSilences: true,
    onSelectionChange: vi.fn(),
    onSelectAllVisible: vi.fn(),
    onRetry: vi.fn(),
    onAck: vi.fn(),
    onResolve: vi.fn(),
    onResolveNodeAlerts: vi.fn(),
    onToggleDeliveries: vi.fn(),
    onRetryDelivery: vi.fn(),
    onRetryAllFailed: vi.fn(),
    onSilence: vi.fn(),
    silenceReturnRef: { current: null },
    silenceFocusScopeRef: { current: null },
    ...overrides,
  };
  // Config links inside the delivery panel are router links.
  return <MemoryRouter><AlertList {...props} /></MemoryRouter>;
}

describe("AlertList delivery panel escalation diagnosis", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    calls = [];
    authState.token = "tok-admin";
    authState.role = "admin";
    rememberAuthIdentity("tok-admin", "admin");
    clearAuthTransitionBarrier();
    bumpAuthSessionGeneration();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("normalizes the mapped alert-123 ID and requests /alerts/123/escalation-preview only on click", async () => {
    const alert = await loadMappedAlert();
    expect(alert.id).toBe("alert-123");
    render(listUi([alert]));
    await flush();
    expect(previewCalls()).toHaveLength(0);

    // The mobile card and desktop table each mount the shared panel; both carry the action.
    const buttons = screen.getAllByRole("button", { name: DIAGNOSE });
    expect(buttons).toHaveLength(2);
    expect(buttons[0]).not.toBeDisabled();

    fireEvent.click(buttons[0]);
    await waitFor(() => expect(previewCalls()).toHaveLength(1));
    expect(previewCalls()[0].url).toMatch(/\/api\/v1\/alerts\/123\/escalation-preview$/);
    expect(previewCalls()[0].init.method).toBe("GET");

    previewCalls()[0].resolve(envelope(validAlertPreviewResponse));
    await flush();
    expect(screen.getByText("Production Escalation")).toBeInTheDocument();
    expect(screen.getByText("Webhook Primary")).toBeInTheDocument();
  });

  it("drops the diagnosis when the parent clears alerts for a refresh, even if fields are unchanged", async () => {
    const alert = await loadMappedAlert();
    const { rerender } = render(listUi([alert]));
    fireEvent.click(screen.getAllByRole("button", { name: DIAGNOSE })[0]);
    await waitFor(() => expect(previewCalls()).toHaveLength(1));
    previewCalls()[0].resolve(envelope(validAlertPreviewResponse));
    await flush();
    expect(screen.getByText("Webhook Primary")).toBeInTheDocument();

    // alert-center sets alerts to [] when a refresh starts, then renders the same public record again.
    rerender(listUi([], { deliveryOpenAlertId: alert.id }));
    rerender(listUi([{ ...alert }]));
    await flush();
    expect(screen.queryByText("Webhook Primary")).not.toBeInTheDocument();
    expect(screen.getAllByText(DIAGNOSE_IDLE).length).toBeGreaterThan(0);
    expect(previewCalls()).toHaveLength(1);
  });

  it("disables diagnosis when the alert ID cannot be parsed", async () => {
    const invalid: AlertRecord = { ...(await loadMappedAlert()), id: "invalid-alert-id" };
    render(listUi([invalid]));
    const buttons = screen.getAllByRole("button", { name: DIAGNOSE });
    expect(buttons.every((button) => (button as HTMLButtonElement).disabled)).toBe(true);
    expect(screen.getAllByText(/无效的告警 ID，无法诊断|Invalid alert ID, cannot diagnose/).length).toBeGreaterThan(0);
    await flush();
    expect(previewCalls()).toHaveLength(0);
  });

  it("renders no diagnosis action and sends no diagnosis request for non-admin users", async () => {
    const alert = await loadMappedAlert();
    authState.role = "operator";
    rememberAuthIdentity("tok-admin", "operator");
    render(listUi([alert]));
    await flush();
    expect(screen.queryByRole("button", { name: DIAGNOSE })).not.toBeInTheDocument();
    expect(previewCalls()).toHaveLength(0);
  });
});

describe("AlertList delivery panel config links and timeline", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    calls = [];
    authState.token = "tok-admin";
    authState.role = "admin";
    rememberAuthIdentity("tok-admin", "admin");
    clearAuthTransitionBarrier();
    bumpAuthSessionGeneration();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("links a safe alert-123 to the three config tabs and reads its timeline as 123", async () => {
    const alert = await loadMappedAlert();
    render(listUi([alert]));
    await flush();

    // Mobile card and desktop table share one panel renderer.
    const navs = configNavs();
    expect(navs).toHaveLength(2);
    for (const nav of navs) {
      expect(within(nav).getAllByRole("link").map((link) => link.getAttribute("href"))).toEqual([
        "/app/settings?tab=channels",
        "/app/settings?tab=silences",
        "/app/settings?tab=escalation",
      ]);
      expect(within(nav).getByText(CONFIG_HINT)).toBeInTheDocument();
      expect(within(nav).queryByText("Task execution timeout")).not.toBeInTheDocument();
    }
    expect(timelineCalls().length).toBeGreaterThan(0);
    for (const call of timelineCalls()) expect(call.url).toMatch(/\/api\/v1\/alerts\/123\/escalation-events$/);
    expect(calls.some((call) => call.url.includes("NaN"))).toBe(false);
    expect(previewCalls()).toHaveLength(0);
  });

  it.each(["invalid-alert-id", "alert-9007199254740993", "alert-0", "alert-042", "123abc"])(
    "hides config links and the timeline for unsafe id %s",
    async (id) => {
      const alert: AlertRecord = { ...(await loadMappedAlert()), id };
      render(listUi([alert]));
      await flush();
      expect(configNavs()).toHaveLength(0);
      expect(screen.queryByText(CONFIG_HINT)).not.toBeInTheDocument();
      expect(screen.queryAllByRole("link").filter((link) => link.getAttribute("href")?.startsWith("/app/settings"))).toHaveLength(0);
      expect(timelineCalls()).toHaveLength(0);
      expect(calls.some((call) => call.url.includes("NaN"))).toBe(false);
      expect(screen.getAllByRole("button", { name: DIAGNOSE }).every((button) => (button as HTMLButtonElement).disabled)).toBe(true);
    },
  );

  it.each(["operator", "viewer"] as const)("renders no config links for %s", async (role) => {
    const alert = await loadMappedAlert();
    authState.role = role;
    rememberAuthIdentity("tok-admin", role);
    render(listUi([alert]));
    await flush();
    expect(configNavs()).toHaveLength(0);
    expect(screen.queryByText(CONFIG_HINT)).not.toBeInTheDocument();
  });

  it("renders no config links while Context and the core identity disagree", async () => {
    const alert = await loadMappedAlert();
    rememberAuthIdentity("tok-other", "admin");
    render(listUi([alert]));
    await flush();
    expect(configNavs()).toHaveLength(0);
  });
});

describe("AlertList grouped alert badge", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    calls = [];
    authState.token = "tok-admin";
    authState.role = "admin";
    rememberAuthIdentity("tok-admin", "admin");
    clearAuthTransitionBarrier();
    bumpAuthSessionGeneration();
  });

  afterEach(async () => {
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
    await setLanguage("zh");
  });

  it.each(["zh", "en"] as const)(
    "labels similar alerts (total - 1) with a total-count tooltip in %s on card and table layouts",
    async (lng) => {
      const alert = await loadMappedAlert();
      await setLanguage(lng);
      for (const key of ["notifications.groupBadge", "notifications.groupBadgeTooltip"]) {
        expect(i18n.getResource(lng, "translation", key)).toEqual(expect.any(String));
      }
      const t = i18n.getFixedT(lng);
      const badgeText = t("notifications.groupBadge", { count: 2 });
      const tooltip = t("notifications.groupBadgeTooltip", { count: 3 });
      if (lng === "en") {
        expect(`${badgeText} ${tooltip}`).not.toMatch(/\p{Script=Han}/u);
      }

      render(listUi([alert], { deliveryOpenAlertId: null, groupInfoMap: { [alert.id]: { count: 3 } } }));

      const badges = screen.getAllByTitle(tooltip);
      expect(badges).toHaveLength(2);
      for (const badge of badges) expect(badge).toHaveTextContent(badgeText);
    },
  );
});
