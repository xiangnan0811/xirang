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
        metadataVersion: null, executionStage: "unknown", reasonCode: null,
        sourceNodeId: null, sourcePolicyId: null, sourceTaskId: null, sourceTaskRunId: null,
        targetPolicyId: null,
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

  describe("R16 safe execution history v1 metadata and boundaries", () => {
    it.each([null, undefined, 0, 2, "1", -1, {}])(
      "keeps new fields unknown/null when metadata_version is not exactly 1: %s", async (invalidVersion) => {
        fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
          code: 200, message: "ok", total: 1, page: 1, page_size: 30,
          data: [{
            id: 1, rule_id: 2, event_type: "backup_failed", action_type: "trigger_task",
            result: "success", target_task_id: 10, target_task_run_id: 20,
            metadata_version: invalidVersion,
            execution_stage: "task_run_reserved",
            source_node_id: 30, source_policy_id: 40, source_task_id: 50, source_task_run_id: 60,
          }],
        })));
        const { items } = await api.listLogs("token");
        expect(items[0]).toMatchObject({
          id: 1, ruleId: 2, targetTaskId: 10, targetTaskRunId: 20,
          metadataVersion: null, executionStage: "unknown", reasonCode: null,
          sourceNodeId: null, sourcePolicyId: null, sourceTaskId: null, sourceTaskRunId: null, targetPolicyId: null,
        });
      },
    );

    it("parses valid v1 success stage boundaries and whitelist associations", async () => {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
        code: 200, message: "ok", total: 4, page: 1, page_size: 30,
        data: [
          {
            id: 101, rule_id: 1, event_type: "backup_succeeded", action_type: "pause_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated",
            source_node_id: 11, source_policy_id: 12, source_task_id: 13, source_task_run_id: 14,
            target_policy_id: 15,
          },
          {
            id: 102, rule_id: 1, event_type: "backup_failed", action_type: "disable_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated",
            source_node_id: 21, source_policy_id: 22,
            target_policy_id: 25,
          },
          {
            id: 103, rule_id: 1, event_type: "backup_failed", action_type: "trigger_task", result: "success",
            target_task_id: 38, target_task_run_id: 39,
            metadata_version: 1, execution_stage: "task_run_reserved",
            source_node_id: 31, source_policy_id: 32, source_task_id: 33, source_task_run_id: 34,
          },
          {
            id: 104, rule_id: 1, event_type: "backup_failed", action_type: "send_notification", result: "success",
            metadata_version: 1, execution_stage: "notification_recorded",
            source_node_id: 41, source_policy_id: 42,
          },
        ],
      })));
      const { items } = await api.listLogs("token");
      expect(items[0]).toMatchObject({
        metadataVersion: 1, executionStage: "policy_updated", reasonCode: null,
        sourceNodeId: 11, sourcePolicyId: 12, sourceTaskId: 13, sourceTaskRunId: 14,
        targetPolicyId: 15,
      });
      expect(items[1]).toMatchObject({
        metadataVersion: 1, executionStage: "policy_updated", reasonCode: null,
        sourceNodeId: 21, sourcePolicyId: 22, sourceTaskId: null, sourceTaskRunId: null,
        targetPolicyId: 25,
      });
      expect(items[2]).toMatchObject({
        metadataVersion: 1, executionStage: "task_run_reserved", reasonCode: null,
        targetTaskId: 38, targetTaskRunId: 39,
        sourceNodeId: 31, sourcePolicyId: 32, sourceTaskId: 33, sourceTaskRunId: 34,
        targetPolicyId: null,
      });
      expect(items[3]).toMatchObject({
        metadataVersion: 1, executionStage: "notification_recorded", reasonCode: null,
        sourceNodeId: 41, sourcePolicyId: 42,
        targetPolicyId: null,
      });
    });

    it("parses valid v1 failure stages and validated reason codes", async () => {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
        code: 200, message: "ok", total: 4, page: 1, page_size: 30,
        data: [
          {
            id: 201, rule_id: 1, event_type: "backup_failed", action_type: "trigger_task", result: "error",
            error_code: "ACTION_FAILED", metadata_version: 1,
            execution_stage: "action_validation", reason_code: "INVALID_TARGET",
            source_node_id: 11, source_policy_id: 12, source_task_id: 13, source_task_run_id: 14,
          },
          {
            id: 202, rule_id: 1, event_type: "backup_failed", action_type: "unknown", result: "error",
            error_code: "ACTION_FAILED", metadata_version: 1,
            execution_stage: "action_validation", reason_code: "UNSUPPORTED_ACTION",
            source_node_id: 21, source_policy_id: 22,
          },
          {
            id: 203, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "error",
            error_code: "ACTION_FAILED", metadata_version: 1,
            execution_stage: "action_execution", reason_code: "TARGET_NOT_FOUND",
            source_node_id: 31, source_policy_id: 32,
          },
          {
            id: 204, rule_id: 1, event_type: "backup_failed", action_type: "trigger_task", result: "error",
            error_code: "ACTION_FAILED", metadata_version: 1,
            execution_stage: "action_execution", reason_code: "DEADLINE_EXCEEDED",
            source_node_id: 41, source_policy_id: 42,
          },
        ],
      })));
      const { items } = await api.listLogs("token");
      expect(items[0]).toMatchObject({ metadataVersion: 1, executionStage: "action_validation", reasonCode: "INVALID_TARGET" });
      expect(items[1]).toMatchObject({ metadataVersion: 1, executionStage: "action_validation", reasonCode: "UNSUPPORTED_ACTION" });
      expect(items[2]).toMatchObject({ metadataVersion: 1, executionStage: "action_execution", reasonCode: "TARGET_NOT_FOUND" });
      expect(items[3]).toMatchObject({ metadataVersion: 1, executionStage: "action_execution", reasonCode: "DEADLINE_EXCEEDED" });
    });

    it("normalizes invalid stage and reason combinations to unknown/null while keeping version and associations", async () => {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
        code: 200, message: "ok", total: 6, page: 1, page_size: 30,
        data: [
          // Success cannot have reason
          {
            id: 301, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated", reason_code: "ACTION_FAILED",
            source_node_id: 11, source_policy_id: 12, target_policy_id: 15,
          },
          // Success stage mismatch (notification_recorded on trigger_task)
          {
            id: 302, rule_id: 1, event_type: "backup_failed", action_type: "trigger_task", result: "success",
            metadata_version: 1, execution_stage: "notification_recorded",
            source_node_id: 21,
          },
          // action_execution cannot be applied to send_notification
          {
            id: 303, rule_id: 1, event_type: "backup_failed", action_type: "send_notification", result: "error",
            metadata_version: 1, execution_stage: "action_execution", reason_code: "ACTION_FAILED",
            source_node_id: 31,
          },
          // UNSUPPORTED_ACTION only allowed on unknown action
          {
            id: 304, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "error",
            metadata_version: 1, execution_stage: "action_validation", reason_code: "UNSUPPORTED_ACTION",
            source_node_id: 41,
          },
          // action_validation cannot take action_execution reasons like CONTEXT_CANCELED
          {
            id: 305, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "error",
            metadata_version: 1, execution_stage: "action_validation", reason_code: "CONTEXT_CANCELED",
            source_node_id: 51,
          },
          // Completely unknown stage and reason strings
          {
            id: 306, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "error",
            metadata_version: 1, execution_stage: "FAKE_STAGE", reason_code: "FAKE_REASON",
            source_node_id: 61,
          },
        ],
      })));
      const { items } = await api.listLogs("token");
      for (const item of items) {
        expect(item.metadataVersion).toBe(1);
        expect(item.executionStage).toBe("unknown");
        expect(item.reasonCode).toBeNull();
      }
      // Associations independently filtered even if stage invalid
      expect(items[0]).toMatchObject({ sourceNodeId: 11, sourcePolicyId: 12, targetPolicyId: 15 });
      expect(items[1].sourceNodeId).toBe(21);
      expect(items[2].sourceNodeId).toBe(31);
      expect(items[3].sourceNodeId).toBe(41);
      expect(items[4].sourceNodeId).toBe(51);
      expect(items[5].sourceNodeId).toBe(61);
    });

    it("filters source associations strictly by event whitelist", async () => {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
        code: 200, message: "ok", total: 3, page: 1, page_size: 30,
        data: [
          // anomaly_detected only takes node
          {
            id: 401, rule_id: 1, event_type: "anomaly_detected", action_type: "pause_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated",
            source_node_id: 11, source_policy_id: 12, source_task_id: 13, source_task_run_id: 14,
            target_policy_id: 15,
          },
          // drill_failed only takes policy
          {
            id: 402, rule_id: 1, event_type: "drill_failed", action_type: "pause_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated",
            source_node_id: 21, source_policy_id: 22, source_task_id: 23, source_task_run_id: 24,
            target_policy_id: 25,
          },
          // unknown event takes none
          {
            id: 403, rule_id: 1, event_type: "unknown_event_type", action_type: "pause_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated",
            source_node_id: 31, source_policy_id: 32,
            target_policy_id: 35,
          },
        ],
      })));
      const { items } = await api.listLogs("token");
      expect(items[0]).toMatchObject({
        sourceNodeId: 11, sourcePolicyId: null, sourceTaskId: null, sourceTaskRunId: null,
        targetPolicyId: 15,
      });
      expect(items[1]).toMatchObject({
        sourceNodeId: null, sourcePolicyId: 22, sourceTaskId: null, sourceTaskRunId: null,
        targetPolicyId: 25,
      });
      expect(items[2]).toMatchObject({
        sourceNodeId: null, sourcePolicyId: null, sourceTaskId: null, sourceTaskRunId: null,
        targetPolicyId: 35,
      });
    });

    it("requires source task and run pair to be all-or-none", async () => {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
        code: 200, message: "ok", total: 2, page: 1, page_size: 30,
        data: [
          {
            id: 501, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated",
            source_node_id: 11, source_task_id: 13, source_task_run_id: null,
          },
          {
            id: 502, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "success",
            metadata_version: 1, execution_stage: "policy_updated",
            source_node_id: 21, source_task_id: null, source_task_run_id: 24,
          },
        ],
      })));
      const { items } = await api.listLogs("token");
      expect(items[0]).toMatchObject({ sourceNodeId: 11, sourceTaskId: null, sourceTaskRunId: null });
      expect(items[1]).toMatchObject({ sourceNodeId: 21, sourceTaskId: null, sourceTaskRunId: null });
    });

    it("only permits targetPolicyId on successful pause_policy or disable_policy", async () => {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
        code: 200, message: "ok", total: 3, page: 1, page_size: 30,
        data: [
          // failure has no target policy
          {
            id: 601, rule_id: 1, event_type: "backup_failed", action_type: "pause_policy", result: "error",
            metadata_version: 1, execution_stage: "action_execution", reason_code: "ACTION_FAILED",
            target_policy_id: 99,
          },
          // trigger_task has no target policy
          {
            id: 602, rule_id: 1, event_type: "backup_failed", action_type: "trigger_task", result: "success",
            metadata_version: 1, execution_stage: "task_run_reserved",
            target_policy_id: 99,
          },
          // send_notification has no target policy
          {
            id: 603, rule_id: 1, event_type: "backup_failed", action_type: "send_notification", result: "success",
            metadata_version: 1, execution_stage: "notification_recorded",
            target_policy_id: 99,
          },
        ],
      })));
      const { items } = await api.listLogs("token");
      expect(items[0].targetPolicyId).toBeNull();
      expect(items[1].targetPolicyId).toBeNull();
      expect(items[2].targetPolicyId).toBeNull();
    });
  });
});
