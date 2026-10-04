import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import i18n from "@/i18n";
import type { AutomationRuleLog, AutomationRuleLogPage, AutomationRuleLogQuery } from "@/lib/api/automation-rules";
import type { AutomationRule } from "@/types/domain";
import { AutomationRulesPage } from "./automation-rules-page";
import { AutomationRuleHistory } from "./automation-rules-page.history";

const mocks = vi.hoisted(() => ({
  auth: { role: "admin", token: "FAKE_TOKEN_FOR_TEST_ONLY" },
  list: vi.fn(),
  listLogs: vi.fn<(token: string, query?: AutomationRuleLogQuery, options?: { signal?: AbortSignal }) => Promise<AutomationRuleLogPage>>(),
}));
vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => mocks.auth }));
vi.mock("@/hooks/use-confirm", () => ({ useConfirm: () => ({ confirm: vi.fn(), dialog: null }) }));
vi.mock("@/lib/api/automation-rules", () => ({ createAutomationRulesApi: () => ({ list: mocks.list, listLogs: mocks.listLogs }) }));
const rule: AutomationRule = { id: 4, name: "Policy guard", description: "", eventType: "backup_failed", actionType: "pause_policy", enabled: true, eventFilter: {}, actionConfig: {}, createdAt: "2026-04-01T12:00:00Z", updatedAt: "2026-04-01T12:00:00Z" };
function row(id: number, overrides: Partial<AutomationRuleLog> = {}): AutomationRuleLog {
  return { id, ruleId: 4, eventType: "backup_failed", actionType: "pause_policy", result: "success", createdAt: "2026-04-01T12:00:00Z", errorCode: null, targetTaskId: null, targetTaskRunId: null, ...overrides };
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
function pageSurface() {
  return <MemoryRouter initialEntries={["/app/automation-rules"]}><Routes>
    <Route path="/app/automation-rules" element={<AutomationRulesPage />} />
    <Route path="/app/overview" element={<p>Overview destination</p>} />
  </Routes></MemoryRouter>;
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  mocks.auth.role = "admin";
  mocks.auth.token = "FAKE_TOKEN_FOR_TEST_ONLY";
  mocks.list.mockReset().mockResolvedValue([rule]);
  mocks.listLogs.mockReset().mockResolvedValue(page([]));
});

describe("automation execution history", () => {
  it("resets both filters to page one and ignores aborted older results", async () => {
    const user = userEvent.setup();
    const older = deferred();
    mocks.listLogs.mockResolvedValueOnce(page([row(61)], 1, 61)).mockImplementationOnce(() => older.promise)
      .mockResolvedValueOnce(page([row(62)], 1, 31)).mockResolvedValueOnce(page([row(63)], 2, 31))
      .mockResolvedValueOnce(page([row(64, { result: "error", errorCode: "ACTION_FAILED" })]));
    render(<AutomationRuleHistory token={mocks.auth.token} rules={[rule]} />);
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
    render(<AutomationRuleHistory token={mocks.auth.token} rules={[rule]} />);
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
    const view = render(<AutomationRuleHistory token="old" rules={[rule]} />);
    await screen.findAllByText("Log #10");
    await user.click(screen.getByRole("button", { name: i18n.t("common.refresh") }));
    expect(screen.queryByText("Log #10")).not.toBeInTheDocument();
    view.rerender(<AutomationRuleHistory token="new" rules={[rule]} />);
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
    render(<AutomationRuleHistory token={mocks.auth.token} rules={[rule]} />);
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
});
