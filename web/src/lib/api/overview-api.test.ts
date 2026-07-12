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

  it("getBackupConfidence 对未知状态和非法数字降级到安全默认值", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          summary: {
            healthy: "bad",
            warning: "NaN",
            at_risk: "invalid",
            insufficient: null,
            total: "oops"
          },
          items: [
            {
              id: "policy-bad",
              policy_id: "bad",
              node_id: "NaN",
              status: "unexpected",
              score: "bad-score",
              reasons: [{ code: "unknown", severity: "unexpected", message: "bad severity" }],
              evidence: [{ task_id: "bad", task_run_id: "NaN", alert_id: "" }],
              targets: [{ node_id: "bad", node_name: "node-bad" }]
            }
          ]
        }
      }))
    );

    const result = await api.getBackupConfidence("token-1");

    expect(result.summary).toEqual({ healthy: 0, warning: 0, atRisk: 0, insufficient: 0, total: 0 });
    expect(result.items[0]).toMatchObject({
      policyId: undefined,
      nodeId: undefined,
      status: "insufficient",
      score: 0,
      reasons: [{ code: "unknown", severity: "info", message: "bad severity" }],
    });
    expect(result.items[0].evidence[0]).toMatchObject({ taskId: undefined, taskRunId: undefined, alertId: undefined });
    expect(result.items[0].targets[0]).toEqual({ nodeId: 0, nodeName: "node-bad", lastBackupAt: undefined });
  });

  it("getHealthIncidentTimeline 请求健康事件时间线并映射 camelCase 字段", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          generated_at: "2026-05-17T01:00:00Z",
          window_hours: "72",
          summary: { total: "1", critical: 1, warning: 0, info: 0 },
          groups: [
            {
              id: "task-7",
              severity: "critical",
              resource: {
                type: "task",
                id: "7",
                name: "daily-backup",
                node_id: "3",
                node_name: "node-a",
                policy_id: "5",
                policy_name: "daily-policy"
              },
              last_seen_at: "2026-05-17T00:55:00Z",
              event_count: "2",
              likely_cause: "rsync exited with code 23",
              source_types: ["alert", "task_failure"],
              next_actions: [{ code: "view_task_logs", label: "查看任务日志", href: "/app/logs?task=7" }],
              signals: [
                {
                  type: "task_failure",
                  severity: "critical",
                  occurred_at: "2026-05-17T00:55:00Z",
                  message: "rsync exited with code 23",
                  task_id: "7",
                  task_run_id: "11",
                  node_id: "3",
                  policy_id: "5"
                }
              ]
            }
          ]
        }
      }))
    );

    const result = await api.getHealthIncidentTimeline("token-1", { windowHours: 72 });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/overview/health-incident-timeline?window_hours=72");
    expect(result.generatedAt).toBe("2026-05-17T01:00:00Z");
    expect(result.windowHours).toBe(72);
    expect(result.summary.critical).toBe(1);
    expect(result.groups[0]).toMatchObject({
      id: "task-7",
      severity: "critical",
      lastSeenAt: "2026-05-17T00:55:00Z",
      eventCount: 2,
      likelyCause: "rsync exited with code 23",
      sourceTypes: ["alert", "task_failure"],
    });
    expect(result.groups[0].resource).toMatchObject({
      type: "task",
      id: 7,
      nodeId: 3,
      nodeName: "node-a",
      policyId: 5,
      policyName: "daily-policy",
    });
    expect(result.groups[0].nextActions[0]).toEqual({ code: "view_task_logs", label: "查看任务日志", href: "/app/logs?task=7" });
    expect(result.groups[0].signals[0]).toMatchObject({ type: "task_failure", taskRunId: 11, nodeId: 3, policyId: 5 });
  });

  it("getHealthIncidentTimeline 对缺失数组、未知枚举和非法数字降级到安全默认值", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          window_hours: "invalid",
          summary: { total: "invalid", critical: "NaN", warning: "bad", info: null },
          groups: [
            {
              id: "platform",
              severity: "unexpected",
              resource: { type: "unknown", id: "bad", name: "status-page", node_id: "invalid", policy_id: "0" },
              event_count: "bad",
              source_types: ["unknown-source"],
              next_actions: [{ code: "bad", label: "bad", href: "" }],
              signals: [
                {
                  type: "unknown-source",
                  severity: "unexpected",
                  occurred_at: "2026-05-17T00:00:00Z",
                  message: "bad ids",
                  alert_id: "bad",
                  delivery_id: "0",
                  task_id: "-1",
                  task_run_id: "not-a-number",
                  node_id: "3",
                  policy_id: ""
                }
              ]
            }
          ]
        }
      }))
    );

    const result = await api.getHealthIncidentTimeline("token-1");

    expect(result.windowHours).toBe(0);
    expect(result.summary).toEqual({ total: 0, critical: 0, warning: 0, info: 0 });
    expect(result.groups[0].severity).toBe("warning");
    expect(result.groups[0].eventCount).toBe(0);
    expect(result.groups[0].resource).toMatchObject({ type: "platform", id: undefined, nodeId: undefined, policyId: undefined });
    expect(result.groups[0].sourceTypes).toEqual(["alert"]);
    expect(result.groups[0].nextActions).toEqual([]);
    expect(result.groups[0].signals[0]).toMatchObject({ type: "alert", severity: "warning", alertId: undefined, deliveryId: undefined, taskId: undefined, taskRunId: undefined, nodeId: 3, policyId: undefined });
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
          truncated: true,
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
    expect(result.truncated).toBe(true);
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
