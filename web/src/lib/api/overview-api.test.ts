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

  it("getOverviewSummary 请求 /overview 并映射 currentThroughputMbps", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          totalNodes: 10,
          healthyNodes: 8,
          activePolicies: 3,
          runningTasks: 2,
          failedTasks24h: 1,
          currentThroughputMbps: 42.5
        }
      }))
    );

    const result = await api.getOverviewSummary("token-1");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/overview");
    expect(init.headers).toMatchObject({ Authorization: "Bearer token-1" });
    expect(result.failedTasks24h).toBe(1);
    expect(result.currentThroughputMbps).toBe(42.5);
  });

  it("getOverviewSummary 后端未返回 currentThroughputMbps 时降级为 0", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          totalNodes: 5,
          healthyNodes: 3,
          activePolicies: 1,
          runningTasks: 0,
          failedTasks24h: 0
        }
      }))
    );

    const result = await api.getOverviewSummary("token-1");

    expect(result.currentThroughputMbps).toBe(0);
  });

  it("getBackupConfidence 请求 /overview/backup-confidence 并映射可信度字段", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          generated_at: "2026-05-17T00:00:00Z",
          summary: {
            healthy: "1",
            warning: 0,
            at_risk: "1",
            insufficient: 1,
            total: "3"
          },
          items: [
            {
              id: "policy-7",
              scope: "policy",
              policy_id: "7",
              policy_name: "daily-policy",
              node_id: "9",
              node_name: "node-a",
              status: "at_risk",
              score: "42",
              reasons: [{ code: "drill_failed", severity: "critical", message: "最近恢复演练失败" }],
              evidence: [
                {
                  type: "drill",
                  status: "failed",
                  message: "恢复演练状态 failed",
                  observed_at: "2026-05-17T01:00:00Z",
                  task_id: "3",
                  task_run_id: "11",
                  alert_id: "5"
                }
              ],
              next_steps: [{ code: "rerun_restore_drill", label: "重新执行恢复演练" }],
              targets: [{ node_id: "9", node_name: "node-a", last_backup_at: "2026-05-17T00:30:00Z" }]
            }
          ]
        }
      }))
    );

    const result = await api.getBackupConfidence("token-1");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/overview/backup-confidence");
    expect(result.summary.atRisk).toBe(1);
    expect(result.items[0]).toMatchObject({
      policyId: 7,
      policyName: "daily-policy",
      nodeId: 9,
      status: "at_risk",
      score: 42,
      reasons: [{ code: "drill_failed", severity: "critical", message: "最近恢复演练失败" }],
      nextSteps: [{ code: "rerun_restore_drill", label: "重新执行恢复演练" }],
    });
    expect(result.items[0].evidence[0]).toMatchObject({ type: "drill", taskRunId: 11, alertId: 5 });
    expect(result.items[0].targets[0]).toEqual({ nodeId: 9, nodeName: "node-a", lastBackupAt: "2026-05-17T00:30:00Z" });
  });

  it("getOverviewTraffic 带 window 参数并映射点位", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
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
