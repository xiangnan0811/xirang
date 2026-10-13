import { StrictMode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import i18n from "@/i18n";
import type { AutomationRuleLog, AutomationRuleLogPage, AutomationRuleLogQuery } from "@/lib/api/automation-rules";
import {
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  getAuthSessionGeneration,
  rememberAuthIdentity,
  releaseAuthTransitionBarrier,
} from "@/lib/api/core";
import type { AutomationRule } from "@/types/domain";
import { AutomationRulesPage } from "./automation-rules-page";
import { AutomationRuleHistory } from "./automation-rules-page.history";

const mocks = vi.hoisted(() => ({
  auth: { role: "admin", token: "FAKE_TOKEN_FOR_TEST_ONLY" },
  list: vi.fn(),
  listLogs: vi.fn<(token: string, query?: AutomationRuleLogQuery, options?: { signal?: AbortSignal }) => Promise<AutomationRuleLogPage>>(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));
vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => mocks.auth }));
vi.mock("@/hooks/use-confirm", () => ({ useConfirm: () => ({ confirm: vi.fn(), dialog: null }) }));
vi.mock("@/lib/api/automation-rules", () => ({ createAutomationRulesApi: () => ({ list: mocks.list, listLogs: mocks.listLogs }) }));
vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

const rule: AutomationRule = { id: 4, name: "Policy guard", description: "", eventType: "backup_failed", actionType: "pause_policy", enabled: true, eventFilter: {}, actionConfig: {}, createdAt: "2026-04-01T12:00:00Z", updatedAt: "2026-04-01T12:00:00Z" };
const probes = [
  ["automation.history.copyIdsLabel", "copy-log-{{id}}"],
  ["automation.history.currentRuleName", "current-name:{{name}}"],
  ["automation.history.ruleId", "rule-id:{{id}}"],
] as const;
const pairedIds = "log_id=61\nrule_id=4\ntask_id=12\ntask_run_id=34";

type User = ReturnType<typeof userEvent.setup>;
type PendingWrite = { text: string; resolve: () => void; reject: (error: unknown) => void };
type ClipboardOutcome = "resolve" | "reject";

function row(id: number, overrides: Partial<AutomationRuleLog> = {}): AutomationRuleLog {
  return { id, ruleId: 4, eventType: "backup_failed", actionType: "pause_policy", result: "success", createdAt: "2026-04-01T12:00:00Z", errorCode: null, targetTaskId: null, targetTaskRunId: null, ...overrides };
}
function paired(id = 61, overrides: Partial<AutomationRuleLog> = {}) {
  return row(id, { actionType: "trigger_task", targetTaskId: 12, targetTaskRunId: 34, ...overrides });
}
function page(items: AutomationRuleLog[], current = 1, total = items.length): AutomationRuleLogPage {
  return { items, page: current, pageSize: 30, total };
}
function deferred() {
  let resolve!: (data: AutomationRuleLogPage) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<AutomationRuleLogPage>((ok, fail) => { resolve = ok; reject = fail; });
  return { promise, resolve, reject };
}
function historySurface(token: string, rules: AutomationRule[] = [rule]) {
  return <MemoryRouter><AutomationRuleHistory token={token} rules={rules} /></MemoryRouter>;
}
function pageSurface() {
  return <MemoryRouter initialEntries={["/app/automation-rules"]}><Routes>
    <Route path="/app/automation-rules" element={<AutomationRulesPage />} />
    <Route path="/app/overview" element={<p>Overview destination</p>} />
  </Routes></MemoryRouter>;
}
function copyName(id: number | string) {
  return String(i18n.t("automation.history.copyIdsLabel", { id }));
}
function unknownCopyName() {
  return copyName(String(i18n.t("automation.history.unknown")));
}
function logText(id: number) {
  return String(i18n.t("automation.history.logId", { id }));
}
function historyRow(logLabel: string) {
  const tr = within(screen.getByRole("table")).getByText(logLabel).closest("tr");
  const li = within(screen.getByRole("list", { name: i18n.t("automation.history.title") })).getByText(logLabel, { exact: false }).closest("li");
  if (!(tr instanceof HTMLTableRowElement) || !(li instanceof HTMLLIElement)) throw new Error(`missing history row ${logLabel}`);
  return { tr, li };
}
function expectNoToast() {
  expect(mocks.toastSuccess).not.toHaveBeenCalled();
  expect(mocks.toastError).not.toHaveBeenCalled();
}
function installResolvedClipboard() {
  const writeText = vi.fn<(text: string) => Promise<void>>().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  return writeText;
}
function installDeferredClipboard() {
  const writes: PendingWrite[] = [];
  const writeText = vi.fn((text: string) => {
    let resolve!: () => void;
    let reject!: (error: unknown) => void;
    const promise = new Promise<void>((ok, fail) => { resolve = ok; reject = fail; });
    writes.push({ text: String(text), resolve, reject });
    return promise;
  });
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  return { writeText, writes };
}
async function settleWrite(write: PendingWrite, outcome: ClipboardOutcome) {
  await act(async () => {
    if (outcome === "resolve") write.resolve();
    else write.reject(new Error("clipboard denied"));
    await Promise.resolve();
  });
}
async function clickCopy(user: User, id: number | string) {
  const [button] = screen.getAllByRole("button", { name: copyName(id) });
  if (!button) throw new Error(`missing copy button ${id}`);
  await user.click(button);
}
async function readCopy(user: User, id: number | string, writeText: ReturnType<typeof installResolvedClipboard>) {
  const before = writeText.mock.calls.length;
  await clickCopy(user, id);
  await waitFor(() => expect(writeText).toHaveBeenCalledTimes(before + 1));
  return String(writeText.mock.calls[before]?.[0]);
}
function removeResourceKey(key: string) {
  const bundle = i18n.getResourceBundle("en", "translation") as Record<string, unknown> | undefined;
  if (!bundle) return;
  const parts = key.split(".");
  let cursor: Record<string, unknown> = bundle;
  for (let index = 0; index < parts.length - 1; index += 1) {
    const next = cursor[parts[index] ?? ""];
    if (!next || typeof next !== "object") return;
    cursor = next as Record<string, unknown>;
  }
  delete cursor[parts[parts.length - 1] ?? ""];
}
function installProbes() {
  const restorers: Array<() => void> = [];
  for (const [key, value] of probes) {
    const previous = i18n.getResource("en", "translation", key);
    i18n.addResource("en", "translation", key, value);
    restorers.push(() => {
      if (typeof previous === "string") i18n.addResource("en", "translation", key, previous);
      else removeResourceKey(key);
    });
  }
  return () => { for (const restore of restorers) restore(); };
}

let restoreProbes = () => {};

beforeEach(async () => {
  await i18n.changeLanguage("en");
  restoreProbes = installProbes();
  clearAuthTransitionBarrier();
  bumpAuthSessionGeneration();
  mocks.auth.role = "admin";
  mocks.auth.token = "FAKE_TOKEN_FOR_TEST_ONLY";
  rememberAuthIdentity(mocks.auth.token, "admin");
  mocks.list.mockReset().mockResolvedValue([rule]);
  mocks.listLogs.mockReset().mockResolvedValue(page([]));
  mocks.toastSuccess.mockReset();
  mocks.toastError.mockReset();
  installResolvedClipboard();
});

afterEach(() => {
  restoreProbes();
  clearAuthTransitionBarrier();
  rememberAuthIdentity(null, null);
});

describe("automation execution history", () => {
  it("resets both filters to page one and ignores aborted older results", async () => {
    const user = userEvent.setup();
    const older = deferred();
    mocks.listLogs.mockResolvedValueOnce(page([row(61)], 1, 61)).mockImplementationOnce(() => older.promise)
      .mockResolvedValueOnce(page([row(62)], 1, 31)).mockResolvedValueOnce(page([row(63)], 2, 31))
      .mockResolvedValueOnce(page([row(64, { result: "error", errorCode: "ACTION_FAILED" })]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText("Log #61");
    await user.click(screen.getByRole("button", { name: i18n.t("common.nextPage") }));
    await waitFor(() => expect(mocks.listLogs).toHaveBeenCalledTimes(2));
    expect(screen.queryByText("Log #61")).not.toBeInTheDocument();
    await user.selectOptions(screen.getByRole("combobox", { name: "Filter history by rule" }), "4");
    await screen.findAllByText("Log #62");
    expect(mocks.listLogs.mock.calls[1][2]?.signal?.aborted).toBe(true);
    expect(mocks.listLogs.mock.calls[2][1]).toMatchObject({ ruleId: 4, page: 1, pageSize: 30 });
    await act(async () => { older.resolve(page([row(999)], 2, 61)); });
    expect(screen.queryByText("Log #999")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: i18n.t("common.nextPage") }));
    await screen.findAllByText("Log #63");
    await user.selectOptions(screen.getByRole("combobox", { name: "Filter history by result" }), "error");
    await screen.findAllByText("Log #64");
    expect(mocks.listLogs.mock.calls[4][1]).toMatchObject({ ruleId: 4, result: "error", page: 1 });
    expect(screen.queryByText("Log #63")).not.toBeInTheDocument();
  });

  it("distinguishes request failure from empty results and can retry", async () => {
    const user = userEvent.setup();
    mocks.listLogs.mockRejectedValueOnce(new Error("FAKE_DIAGNOSTIC_FOR_TEST_ONLY")).mockResolvedValueOnce(page([row(12)]));
    render(historySurface(mocks.auth.token));
    expect(await screen.findByRole("alert")).toHaveTextContent(i18n.t("automation.history.loadFailed"));
    expect(screen.queryByText(i18n.t("automation.history.empty"))).not.toBeInTheDocument();
    expect(screen.queryByText("FAKE_DIAGNOSTIC_FOR_TEST_ONLY")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: i18n.t("common.retry") }));
    await screen.findAllByText("Log #12");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("clears rows on refresh and ignores the previous identity's late failure", async () => {
    const user = userEvent.setup();
    const old = deferred();
    mocks.listLogs.mockResolvedValueOnce(page([row(10)])).mockImplementationOnce(() => old.promise).mockResolvedValueOnce(page([row(20)]));
    const view = render(historySurface("old"));
    await screen.findAllByText("Log #10");
    await user.click(screen.getByRole("button", { name: i18n.t("common.refresh") }));
    expect(screen.queryByText("Log #10")).not.toBeInTheDocument();
    view.rerender(historySurface("new"));
    await screen.findAllByText("Log #20");
    expect(mocks.listLogs.mock.calls[1][2]?.signal?.aborted).toBe(true);
    await act(async () => { old.reject(new Error("late old identity failure")); });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(within(screen.getByRole("table")).getByText("Log #20")).toBeInTheDocument();
  });

  it("shows only recorded/dispatch semantics and retains missing-rule historical IDs", async () => {
    mocks.listLogs.mockResolvedValueOnce(page([
      row(1, { ruleId: 99, actionType: "trigger_task", targetTaskId: 12, targetTaskRunId: 34 }),
      row(2, { actionType: "send_notification" }),
      row(3, { actionType: "unknown", eventType: "unknown", result: "unknown" }),
      row(4, { result: "error", errorCode: "ACTION_FAILED" }),
    ]));
    render(historySurface(mocks.auth.token));
    const table = within(await screen.findByRole("table"));
    expect(table.getByText(i18n.t("automation.history.unavailableRule", { id: 99 }))).toBeInTheDocument();
    expect(table.getByText(i18n.t("automation.history.targets", { taskId: 12, runId: 34 }))).toBeInTheDocument();
    expect(table.getByText(i18n.t("automation.history.dispatched"))).toBeInTheDocument();
    expect(table.getByText(i18n.t("automation.history.notificationRecorded"))).toBeInTheDocument();
    const unknownRow = table.getByText("Log #3").closest("tr");
    expect(unknownRow).toHaveTextContent(i18n.t("automation.history.unknown"));
    expect(unknownRow).not.toHaveTextContent(i18n.t("automation.history.recorded"));
    expect(table.getByText(i18n.t("automation.history.safeError"))).toBeInTheDocument();
  });

  it.each(["operator", "viewer"])("redirects %s without requesting rules or logs", async (role) => {
    mocks.auth.role = role;
    render(pageSurface());
    expect(await screen.findByText("Overview destination")).toBeInTheDocument();
    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.listLogs).not.toHaveBeenCalled();
  });

  it("removes history and aborts both requests when admin access is lost", async () => {
    const old = deferred();
    mocks.listLogs.mockImplementationOnce(() => old.promise);
    const view = render(pageSurface());
    await waitFor(() => expect(mocks.listLogs).toHaveBeenCalledTimes(1));
    mocks.auth.role = "viewer";
    view.rerender(pageSurface());
    await screen.findByText("Overview destination");
    expect(mocks.listLogs.mock.calls[0][2]?.signal?.aborted).toBe(true);
    expect(mocks.list.mock.calls[0][1].signal.aborted).toBe(true);
    await act(async () => { old.resolve(page([row(999)])); });
    expect(screen.queryByText("Log #999")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: i18n.t("automation.history.title") })).not.toBeInTheDocument();
  });

  it("copies exactly four safe ids and keeps the current rule name separate from history", async () => {
    const user = userEvent.setup();
    const execCommand = vi.fn(() => true);
    const previousExec = document.execCommand;
    Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
    const leaked = paired(61, { result: "error", errorCode: "ACTION_FAILED" });
    Object.assign(leaked, { error: "FAKE_R16_SECRET", details: "<script>FAKE_R16_SECRET</script>", message: "FAKE_R16_SECRET" });
    mocks.listLogs.mockResolvedValueOnce(page([leaked]));
    const clipboard = installDeferredClipboard();
    try {
      const view = render(historySurface(mocks.auth.token));
      await screen.findAllByText(logText(61));
      const table = screen.getByRole("table");
      expect(within(table).getByText("current-name:Policy guard")).toBeInTheDocument();
      expect(within(table).getByText("rule-id:4")).toBeInTheDocument();
      expect(within(table).queryByText("Policy guard (#4)")).not.toBeInTheDocument();
      expect(within(table).getByText(i18n.t("automation.history.targets", { taskId: 12, runId: 34 }))).toBeInTheDocument();
      expect(within(table).getByText(i18n.t("automation.history.safeError"))).toBeInTheDocument();
      expect(screen.queryByText(/FAKE_R16_SECRET/)).not.toBeInTheDocument();
      expect(screen.getByRole("columnheader", { name: i18n.t("automation.history.details") })).toBeInTheDocument();
      const buttons = screen.getAllByRole("button", { name: copyName(61) });
      expect(buttons).toHaveLength(2);
      for (const button of buttons) {
        expect(button).toBeEnabled();
        expect(button).toHaveTextContent(String(i18n.t("automation.history.copyIds")));
        expect(button.querySelector("[aria-hidden='true']")).not.toBeNull();
      }
      expect(historyRow(logText(61)).li.querySelector("[aria-hidden='true']")).not.toBeNull();
      for (const region of Object.values(historyRow(logText(61)))) {
        const link = within(region).getByRole("link", { name: i18n.t("automation.history.taskLogs") });
        expect(link).toHaveAttribute("href", "/app/logs?task=12");
        expect(link).toHaveAccessibleDescription(i18n.t("automation.history.taskLogsHint"));
      }
      await user.click(buttons[0]);
      expect(clipboard.writeText).toHaveBeenCalledTimes(1);
      expect(clipboard.writes[0]?.text).toBe(pairedIds);
      expect(clipboard.writes[0]?.text).not.toMatch(/FAKE_R16_SECRET|Policy guard|<script|backup_failed|ACTION_FAILED|2026-04-01|[{}]/);
      expectNoToast();
      expect(execCommand).not.toHaveBeenCalled();
      for (const button of screen.getAllByRole("button", { name: copyName(61) })) expect(button).toBeDisabled();
      await settleWrite(clipboard.writes[0]!, "resolve");
      await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
      expect(mocks.toastSuccess).toHaveBeenCalledWith(i18n.t("common.copiedToClipboard"));
      expect(mocks.toastError).not.toHaveBeenCalled();
      for (const button of screen.getAllByRole("button", { name: copyName(61) })) expect(button).toBeEnabled();

      view.rerender(historySurface(mocks.auth.token, [{ ...rule, name: "Renamed guard" }]));
      expect(within(screen.getByRole("table")).getByText("current-name:Renamed guard")).toBeInTheDocument();
      expect(within(screen.getByRole("table")).queryByText("current-name:Policy guard")).not.toBeInTheDocument();
      expect(within(screen.getByRole("table")).getByText("rule-id:4")).toBeInTheDocument();
      await clickCopy(user, 61);
      expect(clipboard.writeText).toHaveBeenCalledTimes(2);
      expect(clipboard.writes[1]?.text).toBe(pairedIds);
      expect(clipboard.writes[1]?.text).not.toContain("Renamed guard");
      expect(clipboard.writes[1]?.text).not.toContain("Policy guard");
      await settleWrite(clipboard.writes[1]!, "resolve");
      expect(execCommand).not.toHaveBeenCalled();
    } finally {
      Object.defineProperty(document, "execCommand", { configurable: true, value: previousExec });
    }
  });

  it("omits task and run ids for notifications, other actions, and missing rules", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    mocks.listLogs.mockResolvedValueOnce(page([
      row(8, { actionType: "send_notification", targetTaskId: 77, targetTaskRunId: 88 }),
      row(11, { actionType: "pause_policy", targetTaskId: 5, targetTaskRunId: 6 }),
      row(70, { ruleId: 99 }),
    ]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(8));
    const notification = historyRow(logText(8));
    const otherAction = historyRow(logText(11));
    const missingRule = historyRow(logText(70));
    for (const region of [notification.tr, notification.li, otherAction.tr, otherAction.li]) {
      expect(region).not.toHaveTextContent(String(i18n.t("automation.history.missingTargets")));
      expect(within(region).queryByRole("link")).not.toBeInTheDocument();
    }
    expect(notification.tr).not.toHaveTextContent(String(i18n.t("automation.history.targets", { taskId: 77, runId: 88 })));
    expect(notification.tr).toHaveTextContent(String(i18n.t("automation.history.notificationRecorded")));
    expect(otherAction.tr).not.toHaveTextContent(String(i18n.t("automation.history.targets", { taskId: 5, runId: 6 })));
    expect(missingRule.tr).toHaveTextContent(String(i18n.t("automation.history.unavailableRule", { id: 99 })));
    expect(missingRule.tr).not.toHaveTextContent("current-name:");
    expect(await readCopy(user, 8, writeText)).toBe("log_id=8\nrule_id=4");
    expect(await readCopy(user, 11, writeText)).toBe("log_id=11\nrule_id=4");
    const historical = await readCopy(user, 70, writeText);
    expect(historical).toBe("log_id=70\nrule_id=99");
    expect(historical).not.toContain("Policy guard");
    expect(writeText.mock.calls.map((call) => String(call[0])).join("\n")).not.toMatch(/task_id|task_run_id|77|88/);
    expect(within(screen.getByRole("table")).queryByRole("link")).not.toBeInTheDocument();
  });

  it("warns on an unpaired trigger_task and does not copy or link those targets", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    const unsafe = Number.MAX_SAFE_INTEGER + 1;
    mocks.listLogs.mockResolvedValueOnce(page([
      row(9, { actionType: "trigger_task", targetTaskId: 77, targetTaskRunId: null }),
      row(10, { actionType: "trigger_task", targetTaskId: 0, targetTaskRunId: -3 }),
      row(13, { actionType: "trigger_task", targetTaskId: unsafe, targetTaskRunId: 34 }),
    ]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(9));
    for (const id of [9, 10, 13]) {
      const { tr, li } = historyRow(logText(id));
      for (const region of [tr, li]) {
        expect(region).toHaveTextContent(String(i18n.t("automation.history.missingTargets")));
        expect(within(region).queryByRole("link")).not.toBeInTheDocument();
      }
    }
    expect(historyRow(logText(9)).tr).not.toHaveTextContent(String(i18n.t("automation.history.targets", { taskId: 77, runId: 34 })));
    expect(historyRow(logText(10)).tr).not.toHaveTextContent(String(i18n.t("automation.history.targets", { taskId: 0, runId: -3 })));
    expect(historyRow(logText(13)).tr).not.toHaveTextContent(String(unsafe));
    expect(await readCopy(user, 9, writeText)).toBe("log_id=9\nrule_id=4");
    expect(await readCopy(user, 10, writeText)).toBe("log_id=10\nrule_id=4");
    const unsafeCopy = await readCopy(user, 13, writeText);
    expect(unsafeCopy).toBe("log_id=13\nrule_id=4");
    expect(unsafeCopy).not.toContain(String(unsafe));
    expect(writeText.mock.calls.map((call) => String(call[0])).join("\n")).not.toMatch(/task_id|task_run_id/);
  });

  it("shows unknown and disables copy when neither historical id is usable", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    mocks.listLogs.mockResolvedValueOnce(page([row(0, { ruleId: 0 })]));
    render(historySurface(mocks.auth.token));
    await screen.findByRole("table");
    expect(screen.queryByText(logText(0))).not.toBeInTheDocument();
    expect(screen.queryByText(String(i18n.t("automation.history.unavailableRule", { id: 0 })))).not.toBeInTheDocument();
    expect(screen.queryByText("rule-id:0")).not.toBeInTheDocument();
    const buttons = screen.getAllByRole("button", { name: unknownCopyName() });
    expect(buttons).toHaveLength(2);
    for (const button of buttons) expect(button).toBeDisabled();
    expect(screen.queryByRole("button", { name: "copy-log-0" })).not.toBeInTheDocument();
    await user.click(buttons[0]);
    expect(writeText).not.toHaveBeenCalled();
    expectNoToast();
  });

  it("omits only the invalid id and still copies the other historical id", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    mocks.listLogs.mockResolvedValueOnce(page([
      row(0, { ruleId: 4 }),
      row(21, { ruleId: 4.2 }),
    ]));
    render(historySurface(mocks.auth.token));
    await screen.findByRole("table");
    expect(screen.queryByText(logText(0))).not.toBeInTheDocument();
    expect(screen.queryByText(String(i18n.t("automation.history.unavailableRule", { id: 4.2 })))).not.toBeInTheDocument();
    expect(screen.queryByText("rule-id:4.2")).not.toBeInTheDocument();
    const unknownButtons = screen.getAllByRole("button", { name: unknownCopyName() });
    expect(unknownButtons).toHaveLength(2);
    for (const button of unknownButtons) expect(button).toBeEnabled();
    await user.click(unknownButtons[0]!);
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(writeText.mock.calls[0]?.[0]).toBe("rule_id=4");
    expect(String(writeText.mock.calls[0]?.[0])).not.toContain("log_id");
    expect(await readCopy(user, 21, writeText)).toBe("log_id=21");
    expect(String(writeText.mock.calls[1]?.[0])).not.toMatch(/rule_id|4\.2/);
  });

  it("reports clipboard rejection once and does not write again", async () => {
    const user = userEvent.setup();
    const execCommand = vi.fn(() => true);
    const previousExec = document.execCommand;
    Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
    const writeText = vi.fn<(text: string) => Promise<void>>().mockRejectedValueOnce(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mocks.listLogs.mockResolvedValueOnce(page([paired()]));
    try {
      render(historySurface(mocks.auth.token));
      await screen.findAllByText(logText(61));
      await clickCopy(user, 61);
      await waitFor(() => expect(mocks.toastError).toHaveBeenCalledTimes(1));
      expect(mocks.toastError).toHaveBeenCalledWith(i18n.t("common.copyFailed"));
      expect(mocks.toastSuccess).not.toHaveBeenCalled();
      expect(writeText).toHaveBeenCalledTimes(1);
      expect(execCommand).not.toHaveBeenCalled();
      expect(screen.getAllByRole("button", { name: copyName(61) })[0]).toBeEnabled();
    } finally {
      Object.defineProperty(document, "execCommand", { configurable: true, value: previousExec });
    }
  });

  it("reports a missing clipboard without retrying", async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    mocks.listLogs.mockResolvedValueOnce(page([paired()]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(61));
    await clickCopy(user, 61);
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith(i18n.t("common.copyFailed")));
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
    expect(mocks.toastError).toHaveBeenCalledTimes(1);
    expect(screen.getAllByRole("button", { name: copyName(61) })[0]).toBeEnabled();
  });

  it("writes once when desktop and mobile copy controls re-enter before the clipboard settles", async () => {
    const user = userEvent.setup();
    mocks.listLogs.mockResolvedValueOnce(page([paired()]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(61));
    const writes: PendingWrite[] = [];
    const writeText = vi.fn((text: string) => {
      let resolve!: () => void;
      let reject!: (error: unknown) => void;
      const promise = new Promise<void>((ok, fail) => { resolve = ok; reject = fail; });
      writes.push({ text: String(text), resolve, reject });
      if (writes.length === 1) {
        const [desktop, mobile] = screen.getAllByRole("button", { name: copyName(61) });
        fireEvent.click(mobile!);
        fireEvent.click(desktop!);
      }
      return promise;
    });
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    await clickCopy(user, 61);
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writes[0]?.text).toBe(pairedIds);
    for (const button of screen.getAllByRole("button", { name: copyName(61) })) expect(button).toBeDisabled();
    expectNoToast();
    await settleWrite(writes[0]!, "resolve");
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.toastError).not.toHaveBeenCalled();
    expect(writeText).toHaveBeenCalledTimes(1);
  });

  it.each(["resolve", "reject"] as const)("ignores a late clipboard %s after the history filter remounts", async (outcome) => {
    const user = userEvent.setup();
    mocks.listLogs.mockResolvedValueOnce(page([paired()])).mockResolvedValueOnce(page([row(62)]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(61));
    const clipboard = installDeferredClipboard();
    await clickCopy(user, 61);
    expect(clipboard.writes[0]?.text).toBe(pairedIds);
    await user.selectOptions(screen.getByRole("combobox", { name: i18n.t("automation.history.ruleFilter") }), "4");
    await screen.findAllByText(logText(62));
    expect(screen.queryByText(logText(61))).not.toBeInTheDocument();
    await settleWrite(clipboard.writes[0]!, outcome);
    expectNoToast();
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
    await clickCopy(user, 62);
    expect(clipboard.writeText).toHaveBeenCalledTimes(2);
    expect(clipboard.writes[1]?.text).toBe("log_id=62\nrule_id=4");
    await settleWrite(clipboard.writes[1]!, "resolve");
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it.each(["resolve", "reject"] as const)("ignores a late clipboard %s after the session generation changes", async (outcome) => {
    const user = userEvent.setup();
    mocks.listLogs.mockResolvedValue(page([paired()]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(61));
    const clipboard = installDeferredClipboard();
    await clickCopy(user, 61);
    const generation = getAuthSessionGeneration();
    bumpAuthSessionGeneration();
    expect(getAuthSessionGeneration()).toBe(generation + 1);
    await settleWrite(clipboard.writes[0]!, outcome);
    expectNoToast();
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
    expect(screen.getAllByRole("button", { name: copyName(61) })[0]).toBeEnabled();
    await clickCopy(user, 61);
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: i18n.t("common.refresh") }));
    await screen.findAllByText(logText(61));
    await clickCopy(user, 61);
    expect(clipboard.writeText).toHaveBeenCalledTimes(2);
    expect(clipboard.writes[1]?.text).toBe(pairedIds);
    await settleWrite(clipboard.writes[1]!, "resolve");
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it.each(["resolve", "reject"] as const)("releases a pending copy during an auth transition and ignores the late clipboard %s", async (outcome) => {
    const user = userEvent.setup();
    mocks.listLogs.mockResolvedValueOnce(page([paired()]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(61));
    const clipboard = installDeferredClipboard();
    await clickCopy(user, 61);
    const barrier = beginAuthTransitionBarrier();
    try {
      await settleWrite(clipboard.writes[0]!, outcome);
      expectNoToast();
      const [button] = screen.getAllByRole("button", { name: copyName(61) });
      expect(button).toBeEnabled();
      await user.click(button!);
      expect(clipboard.writeText).toHaveBeenCalledTimes(1);
    } finally {
      releaseAuthTransitionBarrier(barrier);
    }
    await clickCopy(user, 61);
    expect(clipboard.writeText).toHaveBeenCalledTimes(2);
    await settleWrite(clipboard.writes[1]!, "resolve");
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("does not start a write when the token, role, generation, or transition guard fails", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    mocks.listLogs.mockResolvedValueOnce(page([paired()]));
    render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(61));
    const button = () => screen.getAllByRole("button", { name: copyName(61) })[0];

    rememberAuthIdentity("other-token", "admin");
    expect(button()).toBeEnabled();
    await user.click(button()!);
    expect(writeText).not.toHaveBeenCalled();
    expectNoToast();

    rememberAuthIdentity(mocks.auth.token, "viewer");
    await user.click(button()!);
    expect(writeText).not.toHaveBeenCalled();
    expectNoToast();

    rememberAuthIdentity(mocks.auth.token, "admin");
    const barrier = beginAuthTransitionBarrier();
    try {
      await user.click(button()!);
      expect(writeText).not.toHaveBeenCalled();
      expect(button()).toBeEnabled();
      expectNoToast();
    } finally {
      releaseAuthTransitionBarrier(barrier);
    }

    await clickCopy(user, 61);
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith(i18n.t("common.copiedToClipboard")));
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText.mock.calls[0]?.[0]).toBe(pairedIds);

    bumpAuthSessionGeneration();
    await user.click(button()!);
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(mocks.toastSuccess).toHaveBeenCalledTimes(1);
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it.each(["resolve", "reject"] as const)("ignores a clipboard %s after unmount", async (outcome) => {
    const user = userEvent.setup();
    mocks.listLogs.mockResolvedValueOnce(page([paired()]));
    const view = render(historySurface(mocks.auth.token));
    await screen.findAllByText(logText(61));
    const clipboard = installDeferredClipboard();
    await clickCopy(user, 61);
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
    view.unmount();
    await settleWrite(clipboard.writes[0]!, outcome);
    expectNoToast();
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
  });

  it("copies under StrictMode without a stale mount epoch blocking the write", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    mocks.listLogs.mockResolvedValue(page([paired()]));
    render(<StrictMode>{historySurface(mocks.auth.token)}</StrictMode>);
    await screen.findAllByText(logText(61));
    expect(await readCopy(user, 61, writeText)).toBe(pairedIds);
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.toastError).not.toHaveBeenCalled();
    expect(writeText).toHaveBeenCalledTimes(1);
  });
});
