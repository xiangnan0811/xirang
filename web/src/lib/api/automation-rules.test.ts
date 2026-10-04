import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAutomationRulesApi } from "./automation-rules";

function createMockResponse(body: unknown) {
  return {
    status: 200,
    ok: true,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({ code: 0, message: "ok", data: body })),
  } as unknown as Response;
}

const mappedRule = {
  id: 4,
  name: "pause-on-failure",
  description: "pause on backup failure",
  eventType: "backup_failed",
  eventFilter: { node_id: "1" },
  actionType: "pause_policy",
  actionConfig: { policy_id: "1" },
  enabled: true,
  createdAt: "2026-05-01T00:00:00Z",
  updatedAt: "2026-05-02T00:00:00Z",
};

describe("automation rules api mapping", () => {
  const fetchMock = vi.fn();
  const api = createAutomationRulesApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("parses JSON-string event_filter and action_config into camelCase records", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse([
      {
        id: 4,
        name: "pause-on-failure",
        description: "pause on backup failure",
        event_type: "backup_failed",
        event_filter: "{\"node_id\":\"1\"}",
        action_type: "pause_policy",
        action_config: "{\"policy_id\":\"1\"}",
        enabled: true,
        created_at: "2026-05-01T00:00:00Z",
        updated_at: "2026-05-02T00:00:00Z",
      },
    ]));

    await expect(api.list("token")).resolves.toEqual([mappedRule]);
  });

  it("coerces numeric JSON values and drops malformed JSON without unsafe casts", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      id: 4,
      name: "pause-on-failure",
      event_type: "backup_failed",
      event_filter: "{\"node_id\":1}",
      action_type: "pause_policy",
      action_config: "{not-json",
      enabled: false,
      created_at: "2026-05-01T00:00:00Z",
      updated_at: "2026-05-02T00:00:00Z",
    }));

    const rule = await api.create("token", {
      name: "pause-on-failure",
      eventType: "backup_failed",
      eventFilter: { node_id: "1" },
      actionType: "pause_policy",
      actionConfig: { policy_id: "1" },
      enabled: false,
    });

    expect(rule.eventFilter).toEqual({ node_id: "1" });
    expect(rule.actionConfig).toEqual({});
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      name: "pause-on-failure",
      event_type: "backup_failed",
      event_filter: "{\"node_id\":\"1\"}",
      action_type: "pause_policy",
      action_config: "{\"policy_id\":\"1\"}",
      enabled: false,
    });
  });

  it("JSON-stringifies records on update", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      id: 4,
      name: "pause-on-failure",
      event_type: "backup_failed",
      event_filter: { node_id: "2" },
      action_type: "pause_policy",
      action_config: { policy_id: "9" },
      enabled: true,
      created_at: "2026-05-01T00:00:00Z",
      updated_at: "2026-05-02T00:00:00Z",
    }));

    const rule = await api.update("token", 4, {
      name: "pause-on-failure",
      eventType: "backup_failed",
      eventFilter: { node_id: "2" },
      actionType: "pause_policy",
      actionConfig: { policy_id: "9" },
      enabled: true,
    });

    expect(rule.eventFilter).toEqual({ node_id: "2" });
    expect(rule.actionConfig).toEqual({ policy_id: "9" });
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      name: "pause-on-failure",
      event_type: "backup_failed",
      event_filter: "{\"node_id\":\"2\"}",
      action_type: "pause_policy",
      action_config: "{\"policy_id\":\"9\"}",
      enabled: true,
    });
  });

  it("keeps pagination metadata and only exposes the safe log contract", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      code: 200, message: "ok", total: 31, page: 2, page_size: 30,
      data: [{
        id: 8, rule_id: 4, event_type: "backup_failed", action_type: "trigger_task",
        result: "error", created_at: "2026-04-01T12:00:00Z", error_code: "ACTION_FAILED",
        target_task_id: 5, target_task_run_id: Number.MAX_SAFE_INTEGER,
        error: "FAKE_PASSWORD_FOR_TEST_ONLY", details: "FAKE_PEM_FOR_TEST_ONLY",
      }],
    })));
    const page = await api.listLogs("token", { ruleId: 4, result: "error", page: 2, pageSize: 30 });
    expect(page).toEqual({
      total: 31, page: 2, pageSize: 30,
      items: [{
        id: 8, ruleId: 4, eventType: "backup_failed", actionType: "trigger_task",
        result: "error", createdAt: "2026-04-01T12:00:00Z", errorCode: "ACTION_FAILED",
        targetTaskId: 5, targetTaskRunId: Number.MAX_SAFE_INTEGER,
      }],
    });
    expect(String(fetchMock.mock.calls[0][0])).toContain("rule_id=4&result=error&page=2&page_size=30");
  });

  it.each([null, undefined, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, "12", {}])(
    "rejects the whole target pair when one ID is invalid: %s", async (invalid) => {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
        code: 200, message: "ok", total: 1, page: 1, page_size: 30,
        data: [{ id: 1, rule_id: 2, action_type: "trigger_task", target_task_id: 12, target_task_run_id: invalid }],
      })));
      const { items } = await api.listLogs("token");
      expect(items[0]).toMatchObject({ targetTaskId: null, targetTaskRunId: null, result: "unknown", errorCode: null });
    },
  );

  it("normalizes unknown enums and does not infer task targets from other actions", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      code: 200, message: "ok", total: 2, page: 1, page_size: 30,
      data: [
        { id: 1, rule_id: 2, event_type: "FAKE_EVENT_FOR_TEST_ONLY", action_type: "FAKE_ACTION_FOR_TEST_ONLY", result: "FAKE_RESULT_FOR_TEST_ONLY", error_code: "FAKE_ERROR_FOR_TEST_ONLY" },
        { id: 2, rule_id: 2, action_type: "send_notification", result: "success", error_code: "ACTION_FAILED", target_task_id: 1, target_task_run_id: 2 },
      ],
    })));
    const { items } = await api.listLogs("token");
    expect(items[0]).toMatchObject({ eventType: "unknown", actionType: "unknown", result: "unknown", errorCode: null });
    expect(items[1]).toMatchObject({ targetTaskId: null, targetTaskRunId: null, result: "success", errorCode: null });
    expect(JSON.stringify(items)).not.toContain("FAKE_");
  });
});
