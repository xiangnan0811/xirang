import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createServiceMonitorsApi } from "./service-monitors";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

const RAW_MONITOR = {
  id: 1,
  name: "API",
  description: "public api",
  type: "http",
  target: "https://example.com/health",
  interval_seconds: 60,
  timeout_seconds: 10,
  http_method: "get",
  http_expected_status: 200,
  http_headers: '{"X-Token":"abc"}',
  enabled: true,
  last_status: "up",
  uptime_pct: 99.9,
  last_checked_at: "2026-05-06T10:00:00Z",
  created_at: "2026-05-01T10:00:00Z",
  updated_at: "2026-05-06T10:00:00Z",
};

describe("service-monitors api", () => {
  const fetchMock = vi.fn();
  const api = createServiceMonitorsApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("list 将后端 snake_case 字段映射为前端 camelCase 并解析 http_headers", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: [RAW_MONITOR] }))
    );

    const result = await api.list("token");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({
      id: 1,
      intervalSeconds: 60,
      timeoutSeconds: 10,
      httpMethod: "GET", // 小写 get 应被归一化为大写枚举
      httpExpectedStatus: 200,
      httpHeaderList: [{ key: "X-Token", value: "abc" }],
      lastStatus: "up",
      uptimePct: 99.9,
      lastCheckedAt: "2026-05-06T10:00:00Z",
    });
  });

  it("list 对坏 JSON 的 http_headers 退化为空数组，不抛出", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(
        200,
        JSON.stringify({ code: 0, message: "ok", data: [{ ...RAW_MONITOR, http_headers: "not-json" }] })
      )
    );

    const result = await api.list("token");

    expect(result[0].httpHeaderList).toEqual([]);
  });

  it("create 将 httpHeaderList 序列化为后端 http_headers JSON 字段", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: RAW_MONITOR }))
    );

    await api.create("token", {
      name: "API",
      type: "http",
      target: "https://example.com/health",
      httpHeaderList: [{ key: "X-Token", value: "abc" }],
    });

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body.http_headers).toBe('{"X-Token":"abc"}');
    expect(init.method).toBe("POST");
  });

  it("create 对空 httpHeaderList 发送 http_headers 为 '{}'", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: RAW_MONITOR }))
    );

    await api.create("token", {
      name: "API",
      type: "tcp",
      target: "example.com:8080",
      httpHeaderList: [],
    });

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body.http_headers).toBe("{}");
  });
});
