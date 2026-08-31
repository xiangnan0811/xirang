import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createEscalationApi } from "./escalation";

function createMockResponse(body: unknown) {
  return {
    status: 200,
    ok: true,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({ code: 0, message: "ok", data: body })),
  } as unknown as Response;
}

const policyInput = {
  name: "ops",
  description: "night",
  minSeverity: "warning" as const,
  enabled: true,
  levels: [
    { delaySeconds: 0, integrationIds: [1], severityOverride: "" as const, tags: [] },
    { delaySeconds: 300, integrationIds: [2], severityOverride: "critical" as const, tags: ["pager"] },
  ],
};

const expectedLevelsWire = [
  { delay_seconds: 0, integration_ids: [1], severity_override: "", tags: [] },
  { delay_seconds: 300, integration_ids: [2], severity_override: "critical", tags: ["pager"] },
];

const mappedPolicy = {
  id: 9,
  name: "ops",
  description: "night",
  minSeverity: "warning",
  enabled: true,
  levels: [
    { delaySeconds: 0, integrationIds: [1], severityOverride: "", tags: [] },
    { delaySeconds: 300, integrationIds: [2], severityOverride: "critical", tags: ["pager"] },
  ],
  createdAt: "2026-05-01T00:00:00Z",
  updatedAt: "2026-05-02T00:00:00Z",
};

describe("escalation api mapping", () => {
  const fetchMock = vi.fn();
  const api = createEscalationApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps list responses whose levels are JSON strings into camelCase domain records", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse([
      {
        id: 9,
        name: "ops",
        description: "night",
        min_severity: "warning",
        enabled: true,
        levels: JSON.stringify(expectedLevelsWire),
        created_at: "2026-05-01T00:00:00Z",
        updated_at: "2026-05-02T00:00:00Z",
      },
    ]));

    await expect(api.listEscalationPolicies("token")).resolves.toEqual([mappedPolicy]);
  });

  it("maps list responses whose levels are already arrays", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse([
      {
        id: 9,
        name: "ops",
        description: "night",
        min_severity: "warning",
        enabled: true,
        levels: expectedLevelsWire,
        created_at: "2026-05-01T00:00:00Z",
        updated_at: "2026-05-02T00:00:00Z",
      },
    ]));

    await expect(api.listEscalationPolicies("token")).resolves.toEqual([mappedPolicy]);
  });

  it("treats malformed levels JSON as an empty level list", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      id: 9,
      name: "ops",
      min_severity: "warning",
      enabled: true,
      levels: "{not-json",
      created_at: "2026-05-01T00:00:00Z",
      updated_at: "2026-05-02T00:00:00Z",
    }));

    const policy = await api.getEscalationPolicy("token", 9);
    expect(policy.levels).toEqual([]);
  });

  it("decodes escalation event JSON-string arrays while preserving array compatibility", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse([
      {
        id: 21,
        alert_id: 7,
        escalation_policy_id: 9,
        level_index: 1,
        integration_ids: "[2,3]",
        severity_before: "warning",
        severity_after: "critical",
        tags_added: '["pager","night"]',
        fired_at: "2026-05-03T00:00:00Z",
      },
      {
        id: 22,
        alert_id: 7,
        escalation_policy_id: null,
        level_index: 0,
        integration_ids: [4],
        severity_before: "info",
        severity_after: "warning",
        tags_added: ["array-compatible"],
        fired_at: "2026-05-03T00:01:00Z",
      },
    ]));

    await expect(api.listAlertEscalationEvents("token", 7)).resolves.toEqual([
      expect.objectContaining({ integrationIds: [2, 3], tagsAdded: ["pager", "night"] }),
      expect.objectContaining({ integrationIds: [4], tagsAdded: ["array-compatible"] }),
    ]);
  });

  it("fails closed for malformed or wrong-shaped escalation event arrays", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse([
      {
        id: 23,
        alert_id: 7,
        integration_ids: "{not-json",
        tags_added: '{"tag":"not-an-array"}',
      },
    ]));

    await expect(api.listAlertEscalationEvents("token", 7)).resolves.toEqual([
      expect.objectContaining({ integrationIds: [], tagsAdded: [] }),
    ]);
  });

  it("sends create/update levels as a snake_case array, not a JSON string", async () => {
    fetchMock
      .mockResolvedValueOnce(createMockResponse({
        id: 9,
        name: "ops",
        description: "night",
        min_severity: "warning",
        enabled: true,
        levels: expectedLevelsWire,
        created_at: "2026-05-01T00:00:00Z",
        updated_at: "2026-05-02T00:00:00Z",
      }))
      .mockResolvedValueOnce(createMockResponse({
        id: 9,
        name: "ops",
        description: "night",
        min_severity: "warning",
        enabled: true,
        levels: expectedLevelsWire,
        created_at: "2026-05-01T00:00:00Z",
        updated_at: "2026-05-02T00:00:00Z",
      }));

    await expect(api.createEscalationPolicy("token", policyInput)).resolves.toEqual(mappedPolicy);
    await expect(api.updateEscalationPolicy("token", 9, policyInput)).resolves.toEqual(mappedPolicy);

    const createBody = JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body));
    const updateBody = JSON.parse(String((fetchMock.mock.calls[1][1] as RequestInit).body));
    expect(createBody).toEqual({
      name: "ops",
      description: "night",
      min_severity: "warning",
      enabled: true,
      levels: expectedLevelsWire,
    });
    expect(Array.isArray(createBody.levels)).toBe(true);
    expect(updateBody.levels).toEqual(expectedLevelsWire);
    expect((fetchMock.mock.calls[0][1] as RequestInit).method).toBe("POST");
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe("PATCH");
  });
});
