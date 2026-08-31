import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createReportsApi, mapReport, mapReportConfig } from "./reports-api";

function createMockResponse(body: unknown) {
  return {
    status: 200,
    ok: true,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({ code: 0, message: "ok", data: body })),
  } as unknown as Response;
}

describe("reports-api mapping", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps report config wire fields to camelCase", () => {
    expect(mapReportConfig({
      id: 3,
      name: "weekly",
      scope_type: "tag",
      scope_value: "prod",
      period: "weekly",
      cron: "0 8 * * 1",
      integration_ids: "[1,2]",
      enabled: true,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-02T00:00:00Z",
    })).toEqual({
      id: 3,
      name: "weekly",
      scopeType: "tag",
      scopeValue: "prod",
      period: "weekly",
      cron: "0 8 * * 1",
      integrationIds: [1, 2],
      enabled: true,
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-02T00:00:00Z",
    });
  });

  it("maps report history wire fields and nested failures", () => {
    expect(mapReport({
      id: 9,
      config_id: 3,
      period_start: "2026-01-01",
      period_end: "2026-01-08",
      total_runs: 10,
      success_runs: 9,
      failed_runs: 1,
      success_rate: 90,
      avg_duration_ms: 1200,
      top_failures: '[{"node_name":"n1","task_name":"t1","count":2,"last_err":"boom"}]',
      disk_trend: "[]",
      generated_at: "2026-01-08T00:00:00Z",
      created_at: "2026-01-08T00:00:01Z",
    })).toMatchObject({
      id: 9,
      configId: 3,
      periodStart: "2026-01-01",
      periodEnd: "2026-01-08",
      totalRuns: 10,
      successRuns: 9,
      avgDurationMs: 1200,
      topFailures: [{ nodeName: "n1", taskName: "t1", count: 2, lastErr: "boom" }],
      generatedAt: "2026-01-08T00:00:00Z",
    });
  });

  it("sends camelCase create input as snake_case wire", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      id: 1,
      name: "weekly",
      scope_type: "all",
      scope_value: "",
      period: "weekly",
      cron: "0 8 * * 1",
      integration_ids: "[]",
      enabled: true,
    }));
    const api = createReportsApi();
    await api.createConfig("token", {
      name: "weekly",
      scopeType: "all",
      scopeValue: "",
      period: "weekly",
      cron: "0 8 * * 1",
      integrationIds: [4],
      enabled: true,
    });
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      name: "weekly",
      scope_type: "all",
      scope_value: "",
      period: "weekly",
      cron: "0 8 * * 1",
      integration_ids: [4],
      enabled: true,
    });
  });
});
