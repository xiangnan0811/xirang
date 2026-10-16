import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./core";
import { createReportsApi, mapReport, mapReportConfig } from "./reports-api";

function createMockResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify({ code: status, message: "ok", data: body }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
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

describe("reports-api schedule preview", () => {
  const fetchMock = vi.fn();
  const INVALID = "Invalid report schedule preview response";

  const futureMatch = {
    status: "match",
    reason: "",
    evaluated_at: "2026-10-18T23:59:30.123456789+08:00",
    expires_at: "2026-10-19T00:00:00+08:00",
    timezone: "Local",
    search_until: "2031-10-18T23:59:30.123456789+08:00",
    window_start: "2026-10-19T08:00:00+08:00",
    window_end: "2026-10-19T08:01:00+08:00",
  };

  const currentMatch = {
    status: "match",
    reason: "",
    evaluated_at: "2026-10-15T12:34:45Z",
    expires_at: "2026-10-15T12:35:00Z",
    timezone: "UTC",
    search_until: "2031-10-15T12:34:45Z",
    window_start: "2026-10-15T12:34:00Z",
    window_end: "2026-10-15T12:35:00Z",
  };

  const noMatch = {
    status: "no_match",
    reason: "unsupported_month",
    evaluated_at: "2026-10-15T08:00:10-04:00",
    expires_at: "2026-10-15T08:01:00-04:00",
    timezone: "America/New_York",
    search_until: "2031-10-15T08:00:10-04:00",
    window_start: null,
    window_end: null,
  };

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sends one encoded cron query with the token and signal, and keeps server offsets verbatim", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(futureMatch));
    const controller = new AbortController();

    const preview = await createReportsApi().previewSchedule("token", "0 8 * * 1&now=1", {
      signal: controller.signal,
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const parsed = new URL(url, "http://localhost");
    expect(parsed.pathname).toMatch(/\/report-configs\/schedule-preview$/);
    expect([...parsed.searchParams.keys()]).toEqual(["cron"]);
    expect(parsed.searchParams.get("cron")).toBe("0 8 * * 1&now=1");
    expect(init.method).toBe("GET");
    expect(init.body).toBeUndefined();
    expect(init.signal).toBe(controller.signal);
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer token");
    expect(preview).toEqual({
      status: "match",
      reason: "",
      evaluatedAt: "2026-10-18T23:59:30.123456789+08:00",
      expiresAt: "2026-10-19T00:00:00+08:00",
      timezone: "Local",
      searchUntil: "2031-10-18T23:59:30.123456789+08:00",
      windowStart: "2026-10-19T08:00:00+08:00",
      windowEnd: "2026-10-19T08:01:00+08:00",
    });
  });

  it("accepts a current matching window that contains the evaluation instant", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(currentMatch));
    await expect(createReportsApi().previewSchedule("token", "* * * * *")).resolves.toMatchObject({
      status: "match",
      windowStart: "2026-10-15T12:34:00Z",
      windowEnd: "2026-10-15T12:35:00Z",
    });
  });

  it("compares mixed offsets by instant rather than by wall-clock text", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      ...futureMatch,
      window_start: "2026-10-19T00:00:00Z",
      window_end: "2026-10-18T19:01:00-05:00",
    }));
    await expect(createReportsApi().previewSchedule("token", "0 8 * * 1")).resolves.toMatchObject({
      windowStart: "2026-10-19T00:00:00Z",
      windowEnd: "2026-10-18T19:01:00-05:00",
    });
  });

  it.each(["unsupported_month", "unmatchable_field", "horizon_exhausted"])(
    "maps no_match/%s without inventing a window",
    async (reason) => {
      fetchMock.mockResolvedValueOnce(createMockResponse({ ...noMatch, reason }));
      await expect(createReportsApi().previewSchedule("token", "0 8 * 2 *")).resolves.toEqual({
        status: "no_match",
        reason,
        evaluatedAt: "2026-10-15T08:00:10-04:00",
        expiresAt: "2026-10-15T08:01:00-04:00",
        timezone: "America/New_York",
        searchUntil: "2031-10-15T08:00:10-04:00",
        windowStart: null,
        windowEnd: null,
      });
    },
  );

  it.each<[string, unknown]>([
    ["null data", null],
    ["array data", [futureMatch]],
    ["unknown status", { ...futureMatch, status: "scheduled" }],
    ["missing status", { ...futureMatch, status: undefined }],
    ["time without offset", { ...futureMatch, evaluated_at: "2026-10-18T23:59:30" }],
    ["date-only time", { ...futureMatch, expires_at: "2026-10-19" }],
    ["non-string time", { ...futureMatch, search_until: 1_950_000_000_000 }],
    ["impossible calendar date", { ...futureMatch, search_until: "2031-02-30T00:00:00+08:00" }],
    ["hour 24", { ...futureMatch, expires_at: "2026-10-18T24:00:00+08:00" }],
    ["out-of-range offset", { ...futureMatch, evaluated_at: "2026-10-18T23:59:30+08:60" }],
    ["lower-case separators", { ...futureMatch, expires_at: "2026-10-19t00:00:00z" }],
    ["empty timezone", { ...futureMatch, timezone: "" }],
    ["blank timezone", { ...futureMatch, timezone: "   " }],
    ["non-string timezone", { ...futureMatch, timezone: 8 }],
    ["expiry equal to evaluation", { ...futureMatch, expires_at: "2026-10-18T23:59:30.123456789+08:00" }],
    ["expiry before evaluation", { ...futureMatch, expires_at: "2026-10-18T23:59:00+08:00" }],
    ["expiry beyond one minute", { ...futureMatch, expires_at: "2026-10-19T00:00:31+08:00" }],
    ["search bound not after evaluation", { ...futureMatch, search_until: "2026-10-18T23:59:30+08:00" }],
    ["match with reason", { ...futureMatch, reason: "unsupported_month" }],
    ["match with missing reason", { ...futureMatch, reason: undefined }],
    ["match with null windows", { ...futureMatch, window_start: null, window_end: null }],
    ["match with two-minute window", { ...futureMatch, window_end: "2026-10-19T08:02:00+08:00" }],
    ["match with reversed window", {
      ...futureMatch,
      window_start: "2026-10-19T08:01:00+08:00",
      window_end: "2026-10-19T08:00:00+08:00",
    }],
    ["match window ended at evaluation", {
      ...currentMatch,
      window_start: "2026-10-15T12:33:45Z",
      window_end: "2026-10-15T12:34:45Z",
    }],
    ["match window at search bound", {
      ...futureMatch,
      window_start: "2031-10-18T23:59:30.123456789+08:00",
      window_end: "2031-10-19T00:00:30.123456789+08:00",
    }],
    ["no_match with unknown reason", { ...noMatch, reason: "disabled" }],
    ["no_match with empty reason", { ...noMatch, reason: "" }],
    ["no_match with inherited reason key", { ...noMatch, reason: "toString" }],
    ["no_match with a window", {
      ...noMatch,
      window_start: "2026-10-15T09:00:00-04:00",
      window_end: "2026-10-15T09:01:00-04:00",
    }],
    ["no_match with missing window fields", { ...noMatch, window_start: undefined, window_end: undefined }],
  ])("rejects a contradictory or malformed DTO: %s", async (_label, body) => {
    fetchMock.mockResolvedValueOnce(createMockResponse(body));
    await expect(createReportsApi().previewSchedule("token", "0 8 * * 1")).rejects.toThrow(INVALID);
  });

  it.each([403, 404, 429])("propagates HTTP %i as ApiError instead of a contract error", async (status) => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: status, message: "denied", data: null }), { status }),
    );
    const pending = createReportsApi().previewSchedule("token", "0 8 * * 1");
    await expect(pending).rejects.toBeInstanceOf(ApiError);
    await expect(pending).rejects.toMatchObject({ status, httpStatus: status });
  });

  it("rejects with AbortError when the transport is cancelled", async () => {
    fetchMock.mockImplementationOnce((_url: string, init: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init.signal?.addEventListener("abort", () => reject(init.signal?.reason), { once: true });
    }));
    const controller = new AbortController();
    const pending = createReportsApi().previewSchedule("token", "0 8 * * 1", { signal: controller.signal });

    controller.abort();

    await expect(pending).rejects.toHaveProperty("name", "AbortError");
  });

  it("does not deliver a response that arrives after cancellation", async () => {
    const controller = new AbortController();
    fetchMock.mockImplementationOnce(async () => {
      controller.abort();
      return createMockResponse(futureMatch);
    });

    await expect(
      createReportsApi().previewSchedule("token", "0 8 * * 1", { signal: controller.signal }),
    ).rejects.toHaveProperty("name", "AbortError");
  });
});
