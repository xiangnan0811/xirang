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

  it("distinguishes an unknown nonempty executor from an absent historical executor", async () => {
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          id: 73,
          name: "future-executor",
          status: "pending",
          node_id: 7,
          executor_type: "future_copy_engine",
          rsync_publication: {
            mode: "legacy_mutable",
            state: "legacy",
            reason_code: "legacy",
          },
        },
      })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          id: 74,
          name: "historical-rsync",
          status: "pending",
          node_id: 7,
        },
      })));

    const unknown = await api.getTask("token", 73);
    const historical = await api.getTask("token", 74);

    expect(unknown.executorType).toBeUndefined();
    expect(unknown.rsyncPublication).toBeUndefined();
    expect(historical.executorType).toBe("rsync");
    expect(historical.rsyncPublication).toMatchObject({
      mode: "legacy_mutable",
      state: "legacy",
      reasonCode: "legacy",
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

describe("Rclone versioning task API", () => {
  const fetchMock = vi.fn();
  const api = createTasksApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const rawReadySummary = {
    mode: "native_object_versions",
    state: "ready",
    reason_code: "ready",
    task_revision: "9007199254740993",
    binding_revision: "7",
    capability_revision: "11",
    consistency_class: "provider_strong",
    hash_fidelity: "download_verified_bytes",
    estimated_read_bytes: "1099511627776",
    api_cost_class: "moderate",
    storage_cost_class: "low",
    egress_cost_class: "high",
    credential_expires_at: "2026-07-17T10:00:00Z",
    encryption_profile: "sse_kms_cmk",
    kms_key_status: "ready",
    kms_read_key_count: 2,
    rollback_locator_present: true,
    rollback_capability: "clean_available",
  };

  it("maps the complete safe Rclone summary and drops provider-private fields", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: {
        id: 81,
        name: "nightly-rclone",
        status: "pending",
        node_id: 7,
        executor_type: "rclone",
        rclone_publication: {
          ...rawReadySummary,
          profile_code: "aws_s3_general_purpose_v1",
          internal_encryption_profile: "sse_kms_cmk_v1",
          bucket: "private-bucket",
          managed_prefix: "private-prefix/",
          role_arn: "arn:aws:iam::123456789012:role/private",
          version_id: "private-version",
          evidence_digest: "private-digest",
        },
      },
    })));

    const task = await api.getTask("token", 81);

    expect(task.rclonePublication).toEqual({
      mode: "native_object_versions",
      state: "ready",
      reasonCode: "ready",
      taskRevision: "9007199254740993",
      bindingRevision: "7",
      capabilityRevision: "11",
      consistencyClass: "provider_strong",
      hashFidelity: "download_verified_bytes",
      estimatedReadBytes: "1099511627776",
      apiCostClass: "moderate",
      storageCostClass: "low",
      egressCostClass: "high",
      credentialExpiresAt: "2026-07-17T10:00:00Z",
      encryptionProfile: "sse_kms_cmk",
      kmsKeyStatus: "ready",
      kmsReadKeyCount: 2,
      rollbackLocatorPresent: true,
      rollbackCapability: "clean_available",
    });
    const safe = JSON.stringify(task.rclonePublication);
    for (const privateValue of ["aws_s3_general_purpose_v1", "_v1", "private-bucket", "private-prefix", "role/private", "private-version", "private-digest"]) {
      expect(safe).not.toContain(privateValue);
    }
  });

  it("fails the enclosing Rclone summary closed for unknown values and never opens unknown rollback", async () => {
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          id: 82,
          name: "unknown-rclone",
          status: "pending",
          node_id: 7,
          executor_type: "rclone",
          rclone_publication: { ...rawReadySummary, state: "future_state" },
        },
      })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          id: 83,
          name: "unknown-rollback",
          status: "pending",
          node_id: 7,
          executor_type: "rclone",
          rclone_publication: { ...rawReadySummary, rollback_capability: "future_clean" },
        },
      })));

    const blocked = await api.getTask("token", 82);
    expect(blocked.rclonePublication).toMatchObject({
      mode: "native_object_versions",
      state: "blocked",
      reasonCode: "unsupported_profile",
      rollbackCapability: "preparation_only",
      encryptionProfile: "sse_kms_cmk",
      kmsKeyStatus: "blocked",
    });
    const guarded = await api.getTask("token", 83);
    expect(guarded.rclonePublication).toMatchObject({
      state: "ready",
      reasonCode: "ready",
      rollbackCapability: "preparation_only",
    });
  });

  it("fails impossible Rclone encryption and KMS combinations closed", async () => {
    const impossible = [
      { ...rawReadySummary, mode: "versioned_prefix", encryption_profile: "sse_kms_cmk" },
      { ...rawReadySummary, encryption_profile: "sse_s3", kms_key_status: "ready", kms_read_key_count: 0 },
      { ...rawReadySummary, kms_key_status: "not_applicable" },
      {
        ...rawReadySummary,
        mode: "versioned_prefix",
        encryption_profile: "none",
        kms_key_status: "not_applicable",
        kms_read_key_count: 1,
      },
    ];

    for (const [index, summary] of impossible.entries()) {
      fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          id: 90 + index,
          name: `invalid-rclone-${index}`,
          status: "pending",
          node_id: 7,
          executor_type: "rclone",
          rclone_publication: summary,
        },
      })));
      const task = await api.getTask("token", 90 + index);
      expect(task.rclonePublication).toMatchObject({
        mode: "native_object_versions",
        state: "blocked",
        reasonCode: "unsupported_profile",
        encryptionProfile: "sse_kms_cmk",
        kmsKeyStatus: "blocked",
        kmsReadKeyCount: 0,
      });
    }
  });

  it("serializes all eight Rclone workflow requests and never synthesizes secrets into responses", async () => {
    const summaryEnvelope = (summary = rawReadySummary) => createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: summary }));
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: {
        setup_id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", expires_at: "2026-07-17T10:00:00Z",
      } })))
      .mockResolvedValueOnce(summaryEnvelope())
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: {
        setup_id: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", expires_at: "2026-07-17T10:00:00Z",
        external_id: "xirang-cccccccccccccccccccccccccccccccc",
      } })))
      .mockResolvedValueOnce(summaryEnvelope())
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: {
        preflight_id: "dddddddddddddddddddddddddddddddd", expires_at: "2026-07-17T10:00:00Z", summary: rawReadySummary,
      } })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: {
        migration_choice: "first_new_point", summary: rawReadySummary,
      } })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: { summary: rawReadySummary } })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: { summary: rawReadySummary } })));

    const portableConfig = "[archive]\ntype = s3\nsecret_access_key = FAKE_RCLONE_SECRET_FOR_TEST_ONLY\n";
    const accessKeyId = "FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY";
    const secretAccessKey = "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY";
    const kmsKeyArn = "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-KEY-FOR-TEST-ONLY";
    const setupPortable = await api.createRclonePortableBindingSetup("token", 7, { expectedTaskRevision: "9007199254740993" });
    const portable = await api.setRclonePortableBinding("token", 7, {
      expectedTaskRevision: "9007199254740993",
      expectedBindingRevision: "0",
      setupId: setupPortable.setupId,
      targetRemote: "archive",
      managedRootLocator: "archive:managed/v1",
      boundConfig: portableConfig,
    });
    const setupNative = await api.createRcloneNativeBindingSetup("token", 7, { expectedTaskRevision: "9007199254740993" });
    const native = await api.setRcloneNativeBinding("token", 7, {
      expectedTaskRevision: "9007199254740993",
      expectedBindingRevision: "0",
      setupId: setupNative.setupId,
      region: "us-east-1",
      bucket: "private-bucket",
      managedPrefix: "managed/v1/",
      roleArn: "arn:aws:iam::123456789012:role/xirang-rclone",
      bootstrap: { mode: "static_sts_bootstrap", accessKeyId, secretAccessKey },
      encryptionProfile: "sse_kms_cmk",
      kmsKeyArn,
    });
    await api.createRcloneVersioningPreflight("token", 7, {
      expectedTaskRevision: "9007199254740993", requestedMode: "native_object_versions",
    });
    await api.activateRcloneVersioning("token", 7, {
      expectedTaskRevision: "9007199254740993", preflightId: "dddddddddddddddddddddddddddddddd", migrationChoice: "first_new_point",
    });
    await api.cleanRollbackRcloneVersioning("token", 7, {
      expectedTaskRevision: "9007199254740994", expectedBindingRevision: "7",
    });
    await api.prepareRcloneVersioningRollback("token", 7, {
      expectedTaskRevision: "9007199254740994", expectedBindingRevision: "7",
    });

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/tasks/7/rclone-versioning/portable-binding-setups",
      "/api/v1/tasks/7/rclone-versioning/portable-binding",
      "/api/v1/tasks/7/rclone-versioning/native-binding-setups",
      "/api/v1/tasks/7/rclone-versioning/native-binding",
      "/api/v1/tasks/7/rclone-versioning/preflights",
      "/api/v1/tasks/7/rclone-versioning/activate",
      "/api/v1/tasks/7/rclone-versioning/clean-rollbacks",
      "/api/v1/tasks/7/rclone-versioning/rollback-preparations",
    ]);
    expect(JSON.parse(String(fetchMock.mock.calls[1][1].body))).toEqual({
      expected_task_revision: "9007199254740993",
      expected_binding_revision: "0",
      setup_id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      target_remote: "archive",
      managed_root_locator: "archive:managed/v1",
      bound_config: portableConfig,
    });
    expect(JSON.parse(String(fetchMock.mock.calls[3][1].body))).toEqual({
      expected_task_revision: "9007199254740993",
      expected_binding_revision: "0",
      setup_id: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      region: "us-east-1",
      bucket: "private-bucket",
      managed_prefix: "managed/v1/",
      role_arn: "arn:aws:iam::123456789012:role/xirang-rclone",
      bootstrap: { mode: "static_sts_bootstrap", access_key_id: accessKeyId, secret_access_key: secretAccessKey },
      encryption_profile: "sse_kms_cmk",
      kms_key_arn: kmsKeyArn,
    });
    const returned = JSON.stringify({ portable, native });
    for (const secret of [portableConfig, accessKeyId, secretAccessKey, kmsKeyArn, "private-bucket", "managed/v1/"]) {
      expect(returned).not.toContain(secret);
    }
  });
});

describe("task inventory pagination", () => {
  const fetchMock = vi.fn();
  const api = createTasksApi();

  function createMockResponse(status = 200, body: unknown = "") {
    return {
      status,
      ok: status >= 200 && status < 300,
      text: vi.fn().mockResolvedValue(typeof body === "string" ? body : JSON.stringify(body)),
      headers: { get: () => null },
    } as unknown as Response;
  }

  function taskRow(id: number) {
    return {
      id,
      name: `task-${id}`,
      status: "success",
      node_id: 1,
      node: { id: 1, name: "node-1" },
    };
  }

  function pageEnvelope(
    items: { id: number; name: string; status: string; node_id: number; node: { id: number; name: string } }[],
    total: number,
    page: number,
    pageSize = 100,
  ) {
    return {
      code: 0,
      message: "ok",
      data: items,
      total,
      page,
      page_size: pageSize,
    };
  }

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("consumes every backend page above the default 100-item page and de-duplicates IDs", async () => {
    const page1 = Array.from({ length: 100 }, (_, i) => taskRow(i + 1));
    const page2 = [taskRow(100), ...Array.from({ length: 99 }, (_, i) => taskRow(i + 101))];
    const page3 = Array.from({ length: 51 }, (_, i) => taskRow(i + 200));

    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, pageEnvelope(page1, 250, 1)))
      .mockResolvedValueOnce(createMockResponse(200, pageEnvelope(page2, 250, 2)))
      .mockResolvedValueOnce(createMockResponse(200, pageEnvelope(page3, 250, 3)));

    const tasks = await api.getTasks("token-tasks");

    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(String(fetchMock.mock.calls[0][0])).toContain("page=1");
    expect(String(fetchMock.mock.calls[0][0])).toContain("page_size=100");
    expect(String(fetchMock.mock.calls[1][0])).toContain("page=2");
    expect(String(fetchMock.mock.calls[2][0])).toContain("page=3");
    expect(tasks).toHaveLength(250);
    expect(new Set(tasks.map((task) => task.id)).size).toBe(250);
    expect(tasks[0]?.id).toBe(1);
    expect(tasks[249]?.id).toBe(250);
  });

  it("stops on an empty page and forwards AbortSignal", async () => {
    const controller = new AbortController();
    fetchMock.mockResolvedValueOnce(createMockResponse(200, pageEnvelope([], 0, 1)));

    const tasks = await api.getTasks("token-tasks", { signal: controller.signal });

    expect(tasks).toEqual([]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect((fetchMock.mock.calls[0][1] as RequestInit).signal).toBe(controller.signal);
  });
});
