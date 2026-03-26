import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createOverviewApi } from "./overview-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(body)
  } as unknown as Response;
}

describe("overview api", () => {
  const fetchMock = vi.fn();
  const api = createOverviewApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("getOverviewSummary 请求 /overview", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        data: {
          totalNodes: 10,
          healthyNodes: 8,
          activePolicies: 3,
          runningTasks: 2,
          failedTasks24h: 1
        }
      }))
    );

    const result = await api.getOverviewSummary("token-1");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/overview");
    expect(init.headers).toMatchObject({ Authorization: "Bearer token-1" });
    expect(result.failedTasks24h).toBe(1);
  });

  it("getOverviewTraffic 带 window 参数并映射点位", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        data: {
          window: "24h",
          bucket_minutes: 30,
          has_real_samples: true,
          generated_at: "2026-03-07T12:00:00Z",
          points: [
            {
              timestamp: "2026-03-07T11:00:00Z",
              timestamp_ms: 1741345200000,
              label: "11:00",
              throughput_mbps: 128,
              sample_count: 2,
              active_task_count: 3,
              started_count: 1,
              failed_count: 0
            }
          ]
        }
      }))
    );

    const result = await api.getOverviewTraffic("token-1", { window: "24h" });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/overview/traffic?window=24h");
    expect(result.window).toBe("24h");
    expect(result.bucketMinutes).toBe(30);
    expect(result.hasRealSamples).toBe(true);
    expect(result.points).toEqual([
      {
        timestamp: "2026-03-07T11:00:00Z",
        timestampMs: 1741345200000,
        label: new Date(1741345200000).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false }),
        throughputMbps: 128,
        sampleCount: 2,
        activeTaskCount: 3,
        startedCount: 1,
        failedCount: 0
      }
    ]);
  });
});
