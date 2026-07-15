import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTasksApi, deriveTaskProgress } from "./tasks-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

describe("deriveTaskProgress", () => {
  it("apiProgress 为 0 时返回 0（有活跃 run 但尚无进度样本）", () => {
    // restore 刚启动：Task.status=success，后端返回 progress=0
    expect(deriveTaskProgress("success", 0, 0, 0)).toBe(0);
  });

  it("apiProgress 为正整数时直接返回（restore 进行中）", () => {
    expect(deriveTaskProgress("success", 0, 0, 45)).toBe(45);
  });

  it("apiProgress 为 undefined 时 success 返回 100（无活跃 run）", () => {
    expect(deriveTaskProgress("success", 0, 0, undefined)).toBe(100);
  });

  it("apiProgress 为 undefined 时 warning 返回 100", () => {
    expect(deriveTaskProgress("warning", 0, 0, undefined)).toBe(100);
  });

  it("apiProgress 为 undefined 时 running 返回 0（不使用虚假值）", () => {
    expect(deriveTaskProgress("running", 0, 0, undefined)).toBe(0);
  });

  it("apiProgress 为 undefined 时 canceled/pending/skipped 返回 0", () => {
    expect(deriveTaskProgress("canceled", 0, 0, undefined)).toBe(0);
    expect(deriveTaskProgress("pending", 0, 0, undefined)).toBe(0);
    expect(deriveTaskProgress("skipped", 0, 0, undefined)).toBe(0);
  });

  it("apiProgress=100 覆盖任何 status（活跃 run 完成）", () => {
    expect(deriveTaskProgress("success", 0, 0, 100)).toBe(100);
    expect(deriveTaskProgress("running", 0, 0, 100)).toBe(100);
  });
});

describe("Rsync versioning task API", () => {
  const fetchMock = vi.fn();
  const api = createTasksApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps the safe Rsync publication summary and drops provider fields", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          id: 71,
          name: "nightly-rsync",
          status: "pending",
          node_id: 7,
          executor_type: "rsync",
          rsync_publication: {
            mode: "versioned_full_copy",
            state: "ready",
            reason_code: "ready",
            capability_revision: 11,
            task_revision: "9007199254740993",
            seed_full_copy_required: false,
            managed_root: "/private/managed-root",
            marker_digest: "private-marker",
          },
        },
      })),
    );

    const task = await api.getTask("token", 71);

    expect(task.rsyncPublication).toEqual({
      mode: "versioned_full_copy",
      state: "ready",
      reasonCode: "ready",
      capabilityRevision: 11,
      taskRevision: "9007199254740993",
      seedFullCopyRequired: false,
    });
    expect(JSON.stringify(task.rsyncPublication)).not.toContain("managed");
    expect(JSON.stringify(task.rsyncPublication)).not.toContain("digest");
  });

  it("fails closed for unknown Rsync publication fields", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          id: 72,
          name: "unknown-rsync",
          status: "pending",
          node_id: 7,
          executor_type: "rsync",
          rsync_publication: {
            mode: "future_mode",
            state: "future_state",
            reason_code: "future_reason",
            capability_revision: 0,
            task_revision: "not-a-revision",
          },
        },
      })),
    );

    const task = await api.getTask("token", 72);

    expect(task.rsyncPublication).toEqual({
      mode: "legacy_mutable",
      state: "blocked",
      reasonCode: "unsupported",
      capabilityRevision: 1,
      taskRevision: "",
      seedFullCopyRequired: false,
    });
  });

  it("serializes only typed Rsync versioning request fields", async () => {
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          preflight_id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          mode: "versioned_full_copy",
          state: "ready",
          reason_code: "ready",
          capability_revision: 11,
          expires_at: "2026-07-15T10:00:00Z",
          capacity_estimate: "available",
          inode_estimate: "constrained",
        },
      })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          migration_choice: "first_new_point",
          summary: {
            mode: "versioned_full_copy",
            state: "ready",
            reason_code: "ready",
            capability_revision: 12,
            task_revision: "9007199254740994",
            seed_full_copy_required: false,
          },
        },
      })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          summary: {
            mode: "versioned_full_copy",
            state: "rollback_prepared",
            reason_code: "rollback_prepared",
            capability_revision: 13,
            task_revision: "9007199254740995",
            seed_full_copy_required: false,
          },
        },
      })));

    await api.createRsyncVersioningPreflight("token", 7, {
      expectedTaskRevision: "9007199254740993",
      requestedMode: "versioned_full_copy",
    });
    const activation = await api.activateRsyncVersioning("token", 7, {
      expectedTaskRevision: "9007199254740993",
      preflightId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      migrationChoice: "first_new_point",
    });
    const rollback = await api.prepareRsyncVersioningRollback("token", 7, {
      expectedTaskRevision: "9007199254740993",
    });
    expect(activation.summary.taskRevision).toBe("9007199254740994");
    expect(rollback.summary.taskRevision).toBe("9007199254740995");

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/tasks/7/rsync-versioning/preflights",
      "/api/v1/tasks/7/rsync-versioning/activate",
      "/api/v1/tasks/7/rsync-versioning/rollback-preparations",
    ]);
    expect(JSON.parse(String(fetchMock.mock.calls[0][1].body))).toEqual({
      expected_task_revision: "9007199254740993",
      requested_mode: "versioned_full_copy",
    });
    expect(JSON.parse(String(fetchMock.mock.calls[1][1].body))).toEqual({
      expected_task_revision: "9007199254740993",
      preflight_id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      migration_choice: "first_new_point",
    });
    expect(JSON.parse(String(fetchMock.mock.calls[2][1].body))).toEqual({
      expected_task_revision: "9007199254740993",
    });
  });
});
