import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import {
  createBackupRecoveryApi,
  generateRecoveryGrantSecret,
  mapRecoveryAuthorizationProduct,
  mapRecoveryJobItemPageProduct,
  mapRecoveryJobProduct,
  mapRecoveryPlanProduct,
  mapRecoveryPreflightProduct,
  mapRecoveryResultPageProduct,
} from "./backup-recovery-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return { ...actual, request: vi.fn() };
});

const requestMock = vi.mocked(request);

const planId = "1".repeat(32);
const repositoryId = "2".repeat(32);
const recoveryPointId = "3".repeat(32);
const preflightId = "4".repeat(32);
const jobId = "5".repeat(32);
const checkpointId = "6".repeat(32);
const attemptId = "7".repeat(32);
const resultSetId = "8".repeat(32);
const resultId = "9".repeat(32);
const grantId = "a".repeat(32);
const receiptId = "b".repeat(32);

function rawPlan() {
  return {
    schema_version: 1,
    id: planId,
    state: "preflight_ready",
    revision: "7",
    repository_id: repositoryId,
    recovery_point_id: recoveryPointId,
    target_mode: "in_place",
    target_node_id: 4,
    target_root_id: "root-1",
    conflict_policy: "exact_mirror",
    security_decision: "allow_clean",
    selection_digest: "a".repeat(64),
    operation_set_digest: "b".repeat(64),
    delete_set_digest: "c".repeat(64),
    estimated_items: 2,
    estimated_bytes: 4096,
    created_at: "2026-08-16T01:02:03Z",
    updated_at: "2026-08-16T01:03:04Z",
  };
}

function rawPreflight() {
  return {
    schema_version: 1,
    plan_id: planId,
    persisted: true,
    plan_revision: "8",
    eligible: true,
    preferred: false,
    reasons: [],
    preflight_id: preflightId,
    preflight_revision: "preflight-v1",
    target_mode: "in_place",
    conflict_policy: "exact_mirror",
    operation_set_digest: "b".repeat(64),
    delete_set_digest: "c".repeat(64),
    impact: {
      create_count: 1,
      overwrite_count: 1,
      skip_count: 0,
      delete_count: 3,
      estimated_items: 5,
      estimated_bytes: 4096,
    },
    security: {
      decision: "allow_clean",
      finding_count: 0,
      overridable_categories: [],
    },
    observed_at: "2026-08-16T01:04:00Z",
    expires_at: "2026-08-16T01:14:00Z",
  };
}

function rawJob(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: 1,
    id: jobId,
    plan_id: planId,
    state: "running",
    revision: "11",
    target_mode: "in_place",
    target_node_id: 4,
    target_root_id: "root-1",
    estimated_items: 5,
    estimated_bytes: 4096,
    progress: {
      total_items: 5,
      completed_items: 2,
      succeeded_items: 1,
      skipped_items: 1,
      failed_items: 0,
      bytes_written: 2048,
    },
    delete_checkpoint: {
      id: checkpointId,
      attempt_id: attemptId,
      expected_plan_revision: "8",
      status: "awaiting_authorization",
      expires_at: "2026-08-16T01:20:00Z",
    },
    created_at: "2026-08-16T01:05:00Z",
    updated_at: "2026-08-16T01:10:00Z",
    ...overrides,
  };
}

describe("backup recovery API boundary", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });
  it("maps one closed snake_case plan product to safe camelCase state", () => {
    const mapped = mapRecoveryPlanProduct({
      schema_version: 1,
      id: planId,
      state: "preflight_ready",
      revision: "7",
      repository_id: repositoryId,
      recovery_point_id: recoveryPointId,
      target_mode: "isolated",
      target_node_id: 4,
      target_root_id: "recovery-root",
      conflict_policy: "fail_on_conflict",
      security_decision: "allow_clean",
      selection_digest: "a".repeat(64),
      operation_set_digest: "b".repeat(64),
      delete_set_digest: "c".repeat(64),
      estimated_items: 2,
      estimated_bytes: 4096,
      created_at: "2026-08-16T01:02:03Z",
      updated_at: "2026-08-16T01:03:04Z",
    });

    expect(mapped).toEqual({
      status: "available",
      value: {
        id: planId,
        state: "preflight_ready",
        revision: "7",
        repositoryId,
        recoveryPointId,
        targetMode: "isolated",
        targetNodeId: 4,
        targetRootId: "recovery-root",
        conflictPolicy: "fail_on_conflict",
        securityDecision: "allow_clean",
        estimatedItems: 2,
        estimatedBytes: 4096,
        createdAt: "2026-08-16T01:02:03.000Z",
        updatedAt: "2026-08-16T01:03:04.000Z",
      },
    });
    expect(JSON.stringify(mapped)).not.toContain("schema_version");
  });

  it("validates live plan summary digests privately without exporting them", () => {
    const mapped = mapRecoveryPlanProduct({
      schema_version: 1,
      id: planId,
      state: "preflight_ready",
      revision: "7",
      repository_id: repositoryId,
      recovery_point_id: recoveryPointId,
      target_mode: "in_place",
      target_node_id: 4,
      target_root_id: "root-1",
      conflict_policy: "exact_mirror",
      security_decision: "allow_clean",
      selection_digest: "a".repeat(64),
      operation_set_digest: "b".repeat(64),
      delete_set_digest: "c".repeat(64),
      estimated_items: 2,
      estimated_bytes: 4096,
      created_at: "2026-08-16T01:02:03Z",
      updated_at: "2026-08-16T01:03:04Z",
    });

    expect(mapped.status).toBe("available");
    expect(JSON.stringify(mapped)).not.toContain("digest");
    expect(mapRecoveryPlanProduct({
      schema_version: 1,
      id: planId,
      state: "preflight_ready",
      revision: "7",
      repository_id: repositoryId,
      recovery_point_id: recoveryPointId,
      target_mode: "isolated",
      target_node_id: 4,
      target_root_id: "r".repeat(33),
      conflict_policy: "fail_on_conflict",
      security_decision: "allow_clean",
      selection_digest: "a".repeat(64),
      operation_set_digest: "b".repeat(64),
      delete_set_digest: "c".repeat(64),
      estimated_items: 2,
      estimated_bytes: 4096,
      created_at: "2026-08-16T01:02:03Z",
      updated_at: "2026-08-16T01:03:04Z",
    })).toEqual({ status: "unavailable", reason: "invalid_product" });
  });

  it("maps complete preflight, authorization, job, item and result products", () => {
    const preflight = mapRecoveryPreflightProduct(rawPreflight());
    expect(preflight).toMatchObject({
      status: "available",
      value: {
        planId,
        persisted: true,
        planRevision: "8",
        preflightId,
        eligible: true,
        impact: { createCount: 1, overwriteCount: 1, deleteCount: 3, estimatedItems: 5 },
        security: { decision: "allow_clean", findingCount: 0, overridableCategories: [] },
      },
    });
    expect(JSON.stringify(preflight)).not.toContain("digest");

    const authorization = mapRecoveryAuthorizationProduct({
      schema_version: 1,
      receipt_id: receiptId,
      plan_id: planId,
      grant_id: grantId,
      grant_category: "write",
      grant_binding_digest: "d".repeat(64),
      grant_expires_at: "2026-08-16T01:19:00Z",
      grant_status: "issued",
      operation: "write_authorize",
      category: "write",
      plan_transition_revision: "9",
      replay: false,
    });
    expect(authorization).toMatchObject({
      status: "available",
      value: {
        receiptId,
        planId,
        grant: { id: grantId, category: "write", status: "issued" },
        operation: "write_authorize",
        planRevision: "9",
        replay: false,
      },
    });
    expect(JSON.stringify(authorization)).not.toContain("binding");

    expect(mapRecoveryJobProduct(rawJob())).toMatchObject({
      status: "available",
      value: {
        id: jobId,
        planId,
        outcome: "running",
        progress: { totalItems: 5, completedItems: 2, bytesWritten: 2048 },
        deleteCheckpoint: { id: checkpointId, attemptId, expectedPlanRevision: "8" },
        resultSet: null,
      },
    });

    expect(mapRecoveryJobItemPageProduct({
      schema_version: 1,
      job_id: jobId,
      page: 1,
      page_size: 25,
      total: 1,
      items: [{
        id: "c".repeat(32), ordinal: 0, operation: "overwrite", outcome: "succeeded",
        estimated_bytes: 4096, bytes_written: 4096, verified_size: 4096,
        created_at: "2026-08-16T01:05:00Z", updated_at: "2026-08-16T01:08:00Z",
      }],
    })).toMatchObject({ status: "available", value: { page: 1, total: 1, items: [{ ordinal: 0 }] } });

    expect(mapRecoveryJobItemPageProduct({
      schema_version: 1,
      job_id: jobId,
      page: 1,
      page_size: 25,
      total: 1,
      items: [{
        id: "d".repeat(32), ordinal: 0, operation: "skip", outcome: "skipped",
        estimated_bytes: 1024, bytes_written: 0, verified_size: 1024,
        created_at: "2026-08-16T01:05:00Z", updated_at: "2026-08-16T01:08:00Z",
      }],
    })).toMatchObject({
      status: "available",
      value: { items: [{ operation: "skip", outcome: "skipped", bytesWritten: 0, verifiedSize: 1024 }] },
    });

    expect(mapRecoveryResultPageProduct({
      schema_version: 1,
      job_id: jobId,
      result_set: {
        id: resultSetId,
        state: "ready",
        plaintext_deadline: "2026-08-17T01:00:00Z",
        hard_deadline: "2026-08-18T01:00:00Z",
        created_at: "2026-08-16T01:00:00Z",
        updated_at: "2026-08-16T01:10:00Z",
      },
      page: 1,
      page_size: 25,
      total: 1,
      items: [{
        id: resultId,
        kind: "regular_file",
        size: 4096,
        created_at: "2026-08-16T01:10:00Z",
      }],
    })).toMatchObject({
      status: "available",
      value: {
        jobId, resultSet: { id: resultSetId, lifecycle: "ready" }, items: [{ id: resultId, modifiedAt: null }],
      },
    });
  });

  it("atomically rejects unknown, dual and contradictory products", () => {
    expect(mapRecoveryPlanProduct({ ...rawPlan(), workspace_phase: "private" })).toEqual({
      status: "unavailable", reason: "invalid_product",
    });
    expect(mapRecoveryPreflightProduct({
      ...rawPreflight(),
      security: { decision: "block", finding_count: 1, overridable_categories: ["unknown"] },
      eligible: true,
    })).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryAuthorizationProduct({
      schema_version: 1,
      receipt_id: receiptId,
      plan_id: planId,
      job_id: jobId,
      operation: "security_override",
      category: "security_override",
      plan_transition_revision: "9",
      replay: false,
    })).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryJobProduct(rawJob({
      state: "succeeded",
      target_mode: "isolated",
      delete_checkpoint: rawJob().delete_checkpoint,
      result_set: {
        id: resultSetId,
        state: "ready",
        plaintext_deadline: "2026-08-17T01:00:00Z",
        hard_deadline: "2026-08-18T01:00:00Z",
        created_at: "2026-08-16T01:00:00Z",
        updated_at: "2026-08-16T01:10:00Z",
      },
    }))).toEqual({ status: "unavailable", reason: "invalid_product" });

    expect(mapRecoveryJobProduct(rawJob({
      state: "succeeded",
      delete_checkpoint: undefined,
      progress: {
        total_items: 5, completed_items: 4, succeeded_items: 3,
        skipped_items: 1, failed_items: 0, bytes_written: 2048,
      },
    }))).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryJobProduct(rawJob({
      state: "degraded",
      delete_checkpoint: undefined,
      failure_category: "partial_write",
      progress: {
        total_items: 5, completed_items: 5, succeeded_items: 4,
        skipped_items: 1, failed_items: 0, bytes_written: 4096,
      },
    }))).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryJobProduct(rawJob({
      state: "succeeded",
      target_mode: "isolated",
      delete_checkpoint: undefined,
      progress: {
        total_items: 5, completed_items: 5, succeeded_items: 4,
        skipped_items: 1, failed_items: 0, bytes_written: 4096,
      },
      result_set: {
        id: resultSetId,
        state: "ready",
        plaintext_deadline: "2026-08-17T01:00:00Z",
        hard_deadline: "2026-08-18T01:00:00Z",
        created_at: "2026-08-17T01:00:00Z",
        updated_at: "2026-08-17T01:00:00Z",
      },
    }))).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryJobProduct(rawJob({
      state: "failed",
      delete_checkpoint: undefined,
    }))).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryJobProduct(rawJob({
      state: "failed",
      delete_checkpoint: undefined,
      failure_category: "source_drift",
    }))).toMatchObject({ status: "available", value: { outcome: "failed", failureCategory: "source_drift" } });

    const itemPage = (item: Record<string, unknown>) => ({
      schema_version: 1,
      job_id: jobId,
      page: 1,
      page_size: 25,
      total: 1,
      items: [{
        id: "d".repeat(32), ordinal: 0, operation: "create", outcome: "succeeded",
        estimated_bytes: 10, bytes_written: 10, verified_size: 10,
        created_at: "2026-08-16T01:05:00Z", updated_at: "2026-08-16T01:08:00Z",
        ...item,
      }],
    });
    expect(mapRecoveryJobItemPageProduct(itemPage({ verified_size: 9 }))).toEqual({
      status: "unavailable", reason: "invalid_product",
    });
    expect(mapRecoveryJobItemPageProduct(itemPage({ bytes_written: 9, verified_size: 9 }))).toEqual({
      status: "unavailable", reason: "invalid_product",
    });
    expect(mapRecoveryJobItemPageProduct(itemPage({
      operation: "overwrite", bytes_written: 9, verified_size: 10,
    }))).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryJobItemPageProduct(itemPage({
      operation: "delete", estimated_bytes: 0, bytes_written: 0, verified_size: 1,
    }))).toEqual({ status: "unavailable", reason: "invalid_product" });
    expect(mapRecoveryJobItemPageProduct(itemPage({ ordinal: 1 }))).toEqual({
      status: "unavailable", reason: "invalid_product",
    });
    expect(mapRecoveryJobItemPageProduct({ ...itemPage({}), total: 2 })).toEqual({
      status: "unavailable", reason: "invalid_product",
    });
  });

  it("generates one canonical 256-bit grant secret and fails closed without Web Crypto", () => {
    const getRandomValues = vi.fn((bytes: Uint8Array) => {
      bytes.forEach((_, index) => { bytes[index] = index; });
      return bytes;
    });
    const randomSpy = vi.spyOn(Math, "random");

    expect(generateRecoveryGrantSecret({ getRandomValues })).toBe(
      "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8",
    );
    expect(getRandomValues).toHaveBeenCalledTimes(1);
    expect(getRandomValues.mock.calls[0]?.[0]).toHaveLength(32);
    expect(randomSpy).not.toHaveBeenCalled();
    expect(() => generateRecoveryGrantSecret(null)).toThrow("secure random unavailable");
  });

  it("uses exact recovery endpoints and caller-owned replay keys, secrets, proofs and signals", async () => {
    const signal = new AbortController().signal;
    const secret = "A".repeat(43);
    requestMock
      .mockResolvedValueOnce({ schema_version: 1, plan_id: planId, state: "draft", replay: false })
      .mockResolvedValueOnce(rawPlan())
      .mockResolvedValueOnce(rawPreflight())
      .mockResolvedValueOnce({
        schema_version: 1, receipt_id: receiptId, plan_id: planId,
        operation: "security_override", category: "security_override",
        plan_transition_revision: "9", replay: false,
      })
      .mockResolvedValueOnce({
        schema_version: 1, receipt_id: receiptId, plan_id: planId, grant_id: grantId,
        grant_category: "write", grant_binding_digest: "d".repeat(64),
        grant_expires_at: "2026-08-16T01:19:00Z", grant_status: "issued",
        operation: "write_authorize", category: "write", plan_transition_revision: "10", replay: false,
      })
      .mockResolvedValueOnce({
        schema_version: 1, receipt_id: receiptId, plan_id: planId, grant_id: grantId, job_id: jobId,
        grant_category: "write", grant_binding_digest: "d".repeat(64),
        grant_expires_at: "2026-08-16T01:19:00Z", grant_status: "consumed",
        operation: "execute", category: "execute", plan_transition_revision: "11", replay: false,
      })
      .mockResolvedValueOnce(rawJob())
      .mockResolvedValueOnce({
        schema_version: 1, receipt_id: receiptId, plan_id: planId, grant_id: grantId, job_id: jobId,
        grant_category: "exact_mirror_delete", grant_binding_digest: "e".repeat(64),
        grant_expires_at: "2026-08-16T01:19:00Z", grant_status: "issued",
        operation: "exact_mirror_delete_authorize", category: "exact_mirror_delete",
        plan_transition_revision: "11", replay: true,
      })
      .mockResolvedValueOnce({
        schema_version: 1, job_id: jobId, page: 2, page_size: 25, total: 26,
        items: [{
          id: "c".repeat(32), ordinal: 25, operation: "skip", outcome: "skipped",
          estimated_bytes: 0, bytes_written: 0, verified_size: 0,
          created_at: "2026-08-16T01:05:00Z", updated_at: "2026-08-16T01:08:00Z",
        }],
      })
      .mockResolvedValueOnce({
        schema_version: 1, job_id: jobId,
        result_set: {
          id: resultSetId, state: "ready", plaintext_deadline: "2026-08-17T01:00:00Z",
          hard_deadline: "2026-08-18T01:00:00Z", created_at: "2026-08-16T01:00:00Z",
          updated_at: "2026-08-16T01:10:00Z",
        },
        page: 1, page_size: 25, total: 1,
        items: [{ id: resultId, kind: "regular_file", size: 4096, created_at: "2026-08-16T01:10:00Z" }],
      })
      .mockResolvedValueOnce({
        schema_version: 1, result_set_id: resultSetId, job_id: jobId, job_revision: "12",
        plaintext_deadline: "2026-08-17T12:00:00Z", hard_deadline: "2026-08-18T01:00:00Z",
      })
      .mockResolvedValueOnce({
        schema_version: 1,
        content_url: `/api/v1/asset-content/${"f".repeat(32)}`,
        action: "download",
        renderer: "attachment",
        profile: "original_v1",
        content_type: "application/octet-stream",
        content_length: 4096,
        etag: '"safe-etag"',
        last_modified: null,
        range: "single",
        classification: "non_secret",
        expires_at: "2026-08-16T01:20:00Z",
        idle_expires_at: "2026-08-16T01:15:00Z",
        capability_reason: null,
        fallback_actions: [],
      })
      .mockResolvedValueOnce(rawPlan())
      .mockResolvedValueOnce(rawJob({ delete_checkpoint: undefined }))
      .mockResolvedValueOnce({
        schema_version: 1, job_id: jobId, result_set_id: resultSetId,
        state: "revoking", scheduled_at: "2026-08-16T01:12:00Z",
      });

    const api = createBackupRecoveryApi();
    await api.createPlan("token", {
      repositoryId, recoveryPointId, catalogGenerationId: "d".repeat(32), entryIds: ["e".repeat(64)],
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
      idempotencyKey: "create-replay-key", signal,
    });
    await api.getPlan("token", planId, signal);
    await api.preflight("token", { planId, expectedRevision: "7", signal });
    await api.overrideSecurity("token", {
      planId, expectedRevision: "8", preflightId, findingCategory: "suspicious",
      reason: "reviewed finding", proof: "override-proof", idempotencyKey: "override-replay-key", signal,
    });
    await api.authorizeWrite("token", {
      planId, expectedRevision: "9", preflightId, reason: "approved write", proof: "write-proof",
      idempotencyKey: "write-replay-key", grantSecret: secret, signal,
    });
    await api.execute("token", {
      planId, expectedRevision: "10", preflightId, grantId, grantSecret: secret,
      proof: "execute-proof", idempotencyKey: "execute-replay-key", signal,
    });
    await api.getJob("token", jobId, signal);
    await api.authorizeExactMirrorDelete("token", {
      jobId, planId, checkpointId, attemptId, expectedRevision: "11", reason: "approved delete",
      proof: "delete-proof", idempotencyKey: "delete-replay-key", grantSecret: secret, signal,
    });
    await api.getJobItems("token", { jobId, page: 2, pageSize: 25, signal });
    await api.getJobResults("token", { jobId, page: 1, pageSize: 25, signal });
    await api.retainResults("token", {
      jobId, expectedRevision: "11", requestedDeadline: "2026-08-17T12:00:00Z",
      proof: "retain-proof", signal,
    });
    await api.issueResultDownloadTicket("token", { jobId, resultId, proof: "download-proof", signal });
    await api.cancelPlan("token", { planId, expectedRevision: "11", signal });
    await api.cancelJob("token", { jobId, expectedRevision: "11", signal });
    await api.cleanupResults("token", { jobId, expectedRevision: "11", signal });

    expect(requestMock).toHaveBeenNthCalledWith(1, "/recovery-plans", {
      method: "POST", token: "token", idempotencyKey: "create-replay-key", signal,
      body: {
        schema_version: 1, repository_id: repositoryId, recovery_point_id: recoveryPointId,
        catalog_generation_id: "d".repeat(32), entry_ids: ["e".repeat(64)], target_mode: "in_place",
        target_node_id: 4, target_root_id: "root-1", conflict_policy: "exact_mirror",
      },
    });
    expect(requestMock).toHaveBeenNthCalledWith(4, `/recovery-plans/${planId}/security-overrides`, {
      method: "POST", token: "token", stepUpProof: "override-proof", idempotencyKey: "override-replay-key", signal,
      body: { schema_version: 1, expected_revision: "8", preflight_id: preflightId, finding_category: "suspicious", reason: "reviewed finding" },
    });
    expect(requestMock).toHaveBeenNthCalledWith(5, `/recovery-plans/${planId}/write-authorizations`, expect.objectContaining({
      idempotencyKey: "write-replay-key", stepUpProof: "write-proof",
      body: expect.objectContaining({ grant_secret: secret }),
    }));
    expect(requestMock).toHaveBeenNthCalledWith(6, `/recovery-plans/${planId}/execute`, expect.objectContaining({
      idempotencyKey: "execute-replay-key", body: expect.objectContaining({ grant_id: grantId, grant_secret: secret }),
    }));
    expect(requestMock).toHaveBeenNthCalledWith(8, `/recovery-jobs/${jobId}/exact-mirror-delete-authorizations`, expect.objectContaining({
      idempotencyKey: "delete-replay-key", stepUpProof: "delete-proof",
      body: expect.objectContaining({ checkpoint_id: checkpointId, attempt_id: attemptId, grant_secret: secret }),
    }));
    expect(requestMock).toHaveBeenNthCalledWith(9, `/recovery-jobs/${jobId}/items?page=2&page_size=25`, {
      token: "token", signal,
    });
    expect(requestMock).toHaveBeenNthCalledWith(10, `/recovery-jobs/${jobId}/results?page=1&page_size=25`, {
      token: "token", signal,
    });
    expect(requestMock).toHaveBeenNthCalledWith(11, `/recovery-jobs/${jobId}/results/retain`, {
      method: "POST", token: "token", stepUpProof: "retain-proof", signal,
      body: {
        schema_version: 1, expected_revision: "11", requested_deadline: "2026-08-17T12:00:00.000Z",
      },
    });
    expect(requestMock).toHaveBeenNthCalledWith(
      12,
      `/recovery-jobs/${jobId}/results/${resultId}/download-ticket`,
      { method: "POST", token: "token", stepUpProof: "download-proof", signal, body: { schema_version: 1 } },
    );
    expect(requestMock).toHaveBeenNthCalledWith(15, `/recovery-jobs/${jobId}/results/cleanup`, {
      method: "POST", token: "token", signal,
      body: { schema_version: 1, expected_revision: "11" },
    });
  });
});
