import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPoliciesApi } from "./policies-api";
import { createTaskRunsApi } from "./task-runs-api";

function createMockResponse(status = 200, payload: unknown = { code: 0, message: "ok", data: null }) {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(JSON.stringify(payload)),
  } as unknown as Response;
}

describe("drill evidence API mapping", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("maps policy latest_drill summary into camelCase domain fields", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, {
        code: 0,
        message: "ok",
        data: [
          {
            id: 7,
            name: "critical-policy",
            source_path: "/data",
            target_path: "/backup",
            cron_spec: "0 2 * * *",
            enabled: true,
            node_ids: [3],
            escalation_policy_id: 9,
            app_profile: "mysql",
            app_credential_id: 11,
            latest_drill: {
              task_run_id: 42,
              status: "failed",
              failed_step: "verify",
              confidence_eligible: false,
              started_at: "2026-05-17T10:00:00Z",
              finished_at: "2026-05-17T10:02:03Z",
              duration_ms: 123000,
            },
          },
        ],
      })
    );

    const rows = await createPoliciesApi().getPolicies("token-policy");

    expect(rows).toHaveLength(1);
    expect(rows[0].latestDrill).toMatchObject({
      taskRunId: 42,
      status: "failed",
      failedStep: "verify",
      confidenceEligible: false,
      startedAt: "2026-05-17T10:00:00Z",
      finishedAt: "2026-05-17T10:02:03Z",
      durationMs: 123000,
    });
    expect(rows[0].escalationPolicyId).toBe(9);
    expect(rows[0].appProfile).toBe("mysql");
    expect(rows[0].appCredentialId).toBe(11);
  });

  it("normalizes invalid latest_drill numbers without NaN", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, {
        code: 0,
        message: "ok",
        data: [
          {
            id: 7,
            name: "bad-drill-policy",
            source_path: "/data",
            target_path: "/backup",
            cron_spec: "0 2 * * *",
            enabled: true,
            node_ids: [3],
            latest_drill: {
              task_run_id: "bad",
              status: "unexpected",
              confidence_eligible: true,
              duration_ms: "NaN",
            },
          },
        ],
      })
    );

    const rows = await createPoliciesApi().getPolicies("token-policy");

    expect(rows[0].latestDrill).toMatchObject({
      taskRunId: 0,
      status: "pending",
      confidenceEligible: true,
      durationMs: 0,
    });
  });

  it("sends advanced policy fields when creating and updating policies", async () => {
    const responsePolicy = {
      id: 8,
      name: "advanced-policy",
      source_path: "/srv/data",
      target_path: "/srv/backup",
      cron_spec: "0 3 * * *",
      enabled: true,
      node_ids: [2],
      escalation_policy_id: 12,
      app_profile: "postgres",
      app_credential_id: 13,
    };
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, { code: 0, message: "ok", data: responsePolicy }))
      .mockResolvedValueOnce(createMockResponse(200, { code: 0, message: "ok", data: responsePolicy }));

    const input = {
      name: "advanced-policy",
      sourcePath: "/srv/data",
      targetPath: "/srv/backup",
      cron: "0 3 * * *",
      criticalThreshold: 2,
      enabled: true,
      nodeIds: [2],
      verifyEnabled: true,
      verifySampleRate: 10,
      escalationPolicyId: 12,
      appProfile: "postgres",
      appCredentialId: 13,
      drillEnabled: true,
      drillCron: "0 4 * * 0",
      drillTargetNodeId: 5,
      drillRestorePath: "/tmp/xirang-drill",
      drillPreVerify: "test -d /tmp/xirang-drill",
      drillVerify: "test -f /tmp/xirang-drill/ok",
      drillPostVerify: "true",
      drillAutoCleanup: true,
    };

    await createPoliciesApi().createPolicy("token-policy", input);
    await createPoliciesApi().updatePolicy("token-policy", 8, input);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const firstBody = JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body));
    const secondBody = JSON.parse(String((fetchMock.mock.calls[1] as [string, RequestInit])[1].body));

    for (const body of [firstBody, secondBody]) {
      expect(body).toMatchObject({
        escalation_policy_id: 12,
        app_profile: "postgres",
        app_credential_id: 13,
        drill_enabled: true,
        drill_cron: "0 4 * * 0",
        drill_target_node_id: 5,
        drill_restore_path: "/tmp/xirang-drill",
        drill_pre_verify: "test -d /tmp/xirang-drill",
        drill_verify: "test -f /tmp/xirang-drill/ok",
        drill_post_verify: "true",
        drill_auto_cleanup: true,
      });
    }
  });

  it("normalizes invalid task run and drill evidence numbers without NaN", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, {
        code: 0,
        message: "ok",
        data: {
          id: "bad",
          task_id: "NaN",
          trigger_type: "drill",
          status: "unexpected",
          duration_ms: "invalid",
          verify_status: "unexpected",
          throughput_mbps: "bad",
          progress: "NaN",
          created_at: "2026-05-17T09:59:59Z",
          drill_evidence: {
            id: "bad",
            policy_id: "NaN",
            task_id: "invalid",
            task_run_id: "oops",
            source_task_run_id: "bad",
            sandbox_node_id: "not-a-number",
            status: "unexpected",
            confidence_eligible: true,
            duration_ms: "NaN",
            created_at: "2026-05-17T09:59:59Z",
          },
        },
      })
    );

    const run = await createTaskRunsApi().getTaskRun("token-task", 42);

    expect(run).toMatchObject({
      id: 0,
      taskId: 0,
      triggerType: "drill",
      status: "pending",
      durationMs: 0,
      throughputMbps: 0,
      progress: 0,
    });
    expect(run.drillEvidence).toMatchObject({
      id: 0,
      policyId: 0,
      taskId: 0,
      taskRunId: 0,
      sourceTaskRunId: null,
      sandboxNodeId: 0,
      status: "pending",
      confidenceEligible: true,
      durationMs: 0,
    });
  });

  it("preserves drill trigger type and maps drill_evidence into camelCase domain fields", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, {
        code: 0,
        message: "ok",
        data: {
          id: 42,
          task_id: 7,
          trigger_type: "drill",
          status: "failed",
          started_at: "2026-05-17T10:00:00Z",
          finished_at: "2026-05-17T10:02:03Z",
          duration_ms: 123000,
          verify_status: "failed",
          throughput_mbps: 0,
          progress: 100,
          created_at: "2026-05-17T09:59:59Z",
          drill_evidence: {
            id: 99,
            policy_id: 3,
            task_id: 7,
            task_run_id: 42,
            source_task_run_id: 40,
            snapshot_ref: "snap-abc",
            sandbox_node_id: 5,
            sandbox_node_name: "sandbox-node",
            sandbox_path: "/tmp/xirang-drill",
            status: "failed",
            failed_step: "cleanup",
            confidence_eligible: false,
            started_at: "2026-05-17T10:00:00Z",
            finished_at: "2026-05-17T10:02:03Z",
            duration_ms: 123000,
            restore_status: "success",
            restore_started_at: "2026-05-17T10:00:05Z",
            restore_finished_at: "2026-05-17T10:01:00Z",
            restore_error: "",
            verify_status: "success",
            verify_started_at: "2026-05-17T10:01:01Z",
            verify_finished_at: "2026-05-17T10:01:30Z",
            verify_error: "",
            post_verify_status: "skipped",
            post_verify_finished_at: "2026-05-17T10:01:31",
            post_verify_error: "",
            cleanup_status: "failed",
            cleanup_started_at: "2026-05-17T10:01:31Z",
            cleanup_finished_at: "2026-05-17T10:02:03Z",
            cleanup_error: "permission denied",
            created_at: "2026-05-17T09:59:59Z",
            updated_at: "2026-05-17T10:02:03Z",
          },
        },
      })
    );

    const run = await createTaskRunsApi().getTaskRun("token-task", 42);

    expect(run.triggerType).toBe("drill");
    expect(run.drillEvidence).toMatchObject({
      id: 99,
      policyId: 3,
      taskId: 7,
      taskRunId: 42,
      sourceTaskRunId: 40,
      snapshotRef: "snap-abc",
      sandboxNodeId: 5,
      sandboxNodeName: "sandbox-node",
      sandboxPath: "/tmp/xirang-drill",
      status: "failed",
      failedStep: "cleanup",
      confidenceEligible: false,
      durationMs: 123000,
      restoreStatus: "success",
      verifyStatus: "success",
      postVerifyStatus: "skipped",
      postVerifyFinishedAt: "2026-05-17 10:01:31",
      cleanupStatus: "failed",
      cleanupError: "permission denied",
    });
  });
});
