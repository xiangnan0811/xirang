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
  name: "pause-offline",
  description: "pause on offline",
  eventType: "node_offline",
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
        name: "pause-offline",
        description: "pause on offline",
        event_type: "node_offline",
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
      name: "pause-offline",
      event_type: "node_offline",
      event_filter: "{\"node_id\":1}",
      action_type: "pause_policy",
      action_config: "{not-json",
      enabled: false,
      created_at: "2026-05-01T00:00:00Z",
      updated_at: "2026-05-02T00:00:00Z",
    }));

    const rule = await api.create("token", {
      name: "pause-offline",
      eventType: "node_offline",
      eventFilter: { node_id: "1" },
      actionType: "pause_policy",
      actionConfig: { policy_id: "1" },
      enabled: false,
    });

    expect(rule.eventFilter).toEqual({ node_id: "1" });
    expect(rule.actionConfig).toEqual({});
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      name: "pause-offline",
      event_type: "node_offline",
      event_filter: "{\"node_id\":\"1\"}",
      action_type: "pause_policy",
      action_config: "{\"policy_id\":\"1\"}",
      enabled: false,
    });
  });

  it("JSON-stringifies records on update", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      id: 4,
      name: "pause-offline",
      event_type: "node_offline",
      event_filter: { node_id: "2" },
      action_type: "pause_policy",
      action_config: { policy_id: "9" },
      enabled: true,
      created_at: "2026-05-01T00:00:00Z",
      updated_at: "2026-05-02T00:00:00Z",
    }));

    const rule = await api.update("token", 4, {
      name: "pause-offline",
      eventType: "node_offline",
      eventFilter: { node_id: "2" },
      actionType: "pause_policy",
      actionConfig: { policy_id: "9" },
      enabled: true,
    });

    expect(rule.eventFilter).toEqual({ node_id: "2" });
    expect(rule.actionConfig).toEqual({ policy_id: "9" });
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      name: "pause-offline",
      event_type: "node_offline",
      event_filter: "{\"node_id\":\"2\"}",
      action_type: "pause_policy",
      action_config: "{\"policy_id\":\"9\"}",
      enabled: true,
    });
  });
});
