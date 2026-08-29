import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import {
  createRecoveryPointsApi,
  mapCatalogStatus,
  mapRecoveryPoint,
  mapRecoveryPointSnapshot,
  mapRecoveryPointEvidence,
} from "./recovery-points-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);
const repositoryId = "1".repeat(32);
const recoveryPointId = "2".repeat(32);
const generationId = "3".repeat(32);

function rawCatalogStatus(expectedEntries: number | null = 0) {
  return {
    generation: {
      id: generationId,
      sequence: 4,
      state: "complete",
      started_at: "2026-07-18T00:00:00Z",
      finished_at: "2026-07-18T00:01:00Z",
      error_code: "",
      correlation_id: "",
    },
    latest_build: {
      id: "4".repeat(32),
      sequence: 5,
      state: "failed",
      started_at: "2026-07-18T02:00:00Z",
      finished_at: "2026-07-18T02:01:00Z",
      error_code: "catalog_projection_mismatch",
      correlation_id: "safe-correlation",
    },
    coverage: {
      status: "complete",
      indexed_entries: 0,
      expected_entries: expectedEntries,
      manifest_digest: "a".repeat(64),
      observed_at: "2026-07-18T08:00:00+08:00",
    },
    staleness: {
      status: "fresh",
      observed_at: "2026-07-18T00:00:00Z",
      reason: null,
    },
    content_availability: {
      available: false,
      reason: { code: "repository_offline", params: {} },
    },
    permissions: { list: true, preview: false, download: false },
    source_fingerprint: "PRIVATE SOURCE FINGERPRINT",
  };
}

function rawRecoveryPoint() {
  return {
    id: recoveryPointId,
    repository_id: repositoryId,
    lineage: {
      producing_task_id: 9,
      producing_task_run_id: 10,
      source_recovery_point_id: "5".repeat(32),
      private_lineage_json: "PRIVATE LINEAGE",
    },
    semantics: "native_snapshot",
    state: "committed",
    physical_availability: "offline",
    hold_state: "none",
    immutability_level: "backend_versioned",
    manifest_digest: "b".repeat(64),
    entry_count: 0,
    logical_bytes: 0,
    captured_at: "2026-07-18T00:00:00Z",
    committed_at: "2026-07-18T00:01:00Z",
    observed_at: null,
    capability_revision: 3,
    capabilities: {
      list: true,
      search_path: false,
      open_sequential: false,
      open_range: false,
      download: false,
      restore: false,
      diff: true,
      native_history: true,
      reason: { code: "repository_offline", params: {} },
    },
    created_at: "2026-07-18T00:00:00Z",
    updated_at: "2026-07-18T00:01:00Z",
    producing_task_name: "nightly",
    producing_node_id: 7,
    producing_node_name: "db-a",
    catalog: rawCatalogStatus(),
    encrypted_rollback_locator: "PRIVATE LOCATOR",
    source_fingerprint: "PRIVATE FINGERPRINT",
  };
}

function rawEvidence() {
  return {
    recovery_point_id: recoveryPointId,
    lineage: {
      status: "recorded",
      task_id: 9,
      task_run_id: 10,
      task_name: "nightly",
      node_id: 7,
      node_name: "db-a",
      trigger: "cron",
      run_status: "success",
      started_at: "2026-07-18T00:00:00Z",
      finished_at: "2026-07-18T00:01:00Z",
      last_error: "PRIVATE ERROR",
    },
    manifest: {
      status: "recorded",
      id: "6".repeat(32),
      revision: 2,
      digest_algorithm: "sha256",
      digest: "c".repeat(64),
      entry_count: 0,
      logical_bytes: 0,
      generator: "xirang",
      generator_version: "v1",
      completeness: "complete",
      created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:01:00Z",
      encrypted_commit_evidence: "PRIVATE EVIDENCE",
    },
    publication_verification: {
      status: "recorded",
      provider: "restic",
      completion: "known_exit_zero",
      failure_code: "",
      capture_started_at: "2026-07-18T00:00:00Z",
      capture_finished_at: "2026-07-18T00:01:00Z",
      files_processed: 0,
      logical_bytes: 0,
      commit_recorded: true,
      raw_provider_output: "PRIVATE OUTPUT",
    },
    restore_drills: {
      status: "recorded",
      items: [
        {
          task_run_id: 11,
          status: "success",
          failed_step: "",
          confidence_eligible: true,
          started_at: "2026-07-18T02:00:00Z",
          finished_at: "2026-07-18T02:01:00Z",
          duration_ms: 60000,
          sandbox_path: "/PRIVATE/SANDBOX",
        },
      ],
    },
  };
}

describe("recovery points API boundary", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("keeps generation, coverage, staleness, and availability separate with known zero", () => {
    const mapped = mapCatalogStatus(rawCatalogStatus(0));

    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") {
      throw new Error("expected available Catalog status");
    }
    expect(mapped.value).toMatchObject({
      generation: { id: generationId, state: "complete" },
      latestBuild: { state: "failed", errorCode: "catalog_projection_mismatch" },
      coverage: {
        status: "complete",
        indexedEntries: 0,
        expectedEntries: 0,
        observedAt: "2026-07-18T00:00:00.000Z",
      },
      staleness: { status: "fresh" },
      contentAvailability: {
        available: false,
        reason: { code: "repository_offline", params: {} },
      },
    });
    expect(JSON.stringify(mapped)).not.toContain("PRIVATE SOURCE FINGERPRINT");
  });

  it("preserves unknown expected count as null rather than converting it to zero", () => {
    const mapped = mapCatalogStatus(rawCatalogStatus(null));
    expect(mapped.status).toBe("available");
    if (mapped.status === "available") {
      expect(mapped.value.coverage.expectedEntries).toBeNull();
    }
  });

  it("fails the whole status projection closed for an unknown enum or invalid required time", () => {
    const unknown = rawCatalogStatus();
    unknown.coverage.status = "future_coverage";
    expect(mapCatalogStatus(unknown)).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });

    const invalidTime = rawCatalogStatus();
    invalidTime.generation.started_at = "not-a-time";
    expect(mapCatalogStatus(invalidTime)).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });

    const unknownErrorCode = rawCatalogStatus();
    unknownErrorCode.latest_build.error_code = "provider_raw_/secret/path";
    expect(mapCatalogStatus(unknownErrorCode)).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("maps a recovery point and drops fingerprints, locators, and raw lineage", () => {
    const mapped = mapRecoveryPoint(rawRecoveryPoint());
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") {
      throw new Error("expected available recovery point");
    }
    expect(mapped.value).toMatchObject({
      id: recoveryPointId,
      repositoryId,
      semantics: "native_snapshot",
      state: "committed",
      physicalAvailability: "offline",
      entryCount: 0,
      logicalBytes: 0,
      producingTaskName: "nightly",
      producingNodeId: 7,
      catalog: { status: "available" },
    });
    const serialized = JSON.stringify(mapped);
    for (const forbidden of ["PRIVATE", "source_fingerprint", "rollback_locator", "lineage_json"]) {
      expect(serialized).not.toContain(forbidden);
    }
  });

  it("shares strict recovery-point snapshot parsing with the full projection", () => {
    const raw = rawRecoveryPoint();
    const snapshot = mapRecoveryPointSnapshot(raw);
    expect(snapshot.status).toBe("available");
    if (snapshot.status !== "available") {
      throw new Error("expected available recovery-point snapshot");
    }
    expect(snapshot.value).toMatchObject({
      id: recoveryPointId,
      repositoryId,
      semantics: "native_snapshot",
      state: "committed",
      capturedAt: "2026-07-18T00:00:00.000Z",
      observedAt: null,
    });
    expect(snapshot.value).not.toHaveProperty("catalog");
    expect(snapshot.value).not.toHaveProperty("producingTaskName");
    expect(JSON.stringify(snapshot)).not.toMatch(/repository_id|captured_at|PRIVATE/);

    expect(mapRecoveryPointSnapshot({ ...raw, semantics: "future_private_semantics" })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(mapRecoveryPointSnapshot({ ...raw, observed_at: "not-a-time" })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(mapRecoveryPoint({ ...raw, captured_at: "not-a-time" })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("maps independent evidence layers without raw errors, output, or sandbox details", () => {
    const mapped = mapRecoveryPointEvidence(rawEvidence());
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") {
      throw new Error("expected available evidence");
    }
    expect(mapped.value).toMatchObject({
      recoveryPointId,
      lineage: { status: "recorded", taskId: 9, taskRunId: 10 },
      manifest: { status: "recorded", entryCount: 0, completeness: "complete" },
      publicationVerification: {
        status: "recorded",
        provider: "restic",
        completion: "known_exit_zero",
        failureCode: null,
        filesProcessed: 0,
      },
      restoreDrills: {
        status: "recorded",
        items: [{ taskRunId: 11, status: "success", confidenceEligible: true }],
      },
    });
    const serialized = JSON.stringify(mapped);
    for (const forbidden of ["PRIVATE", "last_error", "encrypted_commit_evidence", "raw_provider_output", "sandbox_path"]) {
      expect(serialized).not.toContain(forbidden);
    }
  });

  it("blocks the whole evidence projection for unknown nested trigger or run enums", () => {
    const unknownRun = rawEvidence();
    unknownRun.lineage.run_status = "future_private_status";
    expect(mapRecoveryPointEvidence(unknownRun)).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });

    const unknownTrigger = rawEvidence();
    unknownTrigger.lineage.trigger = "future_private_trigger";
    expect(mapRecoveryPointEvidence(unknownTrigger)).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("uses exact point routes, query order, and AbortSignal options", async () => {
    const signal = new AbortController().signal;
    requestMock
      .mockResolvedValueOnce({ items: [rawRecoveryPoint()], next_cursor: "next-point" })
      .mockResolvedValueOnce(rawRecoveryPoint())
      .mockResolvedValueOnce(rawCatalogStatus())
      .mockResolvedValueOnce(rawEvidence());

    const api = createRecoveryPointsApi();
    await api.listRecoveryPoints("token", repositoryId, {
      limit: 25,
      cursor: "point-cursor",
      sort: "captured_asc",
      signal,
    });
    await api.getRecoveryPoint("token", recoveryPointId, signal);
    await api.getRecoveryPointCatalogStatus("token", recoveryPointId, signal);
    await api.getRecoveryPointEvidence("token", recoveryPointId, signal);

    expect(requestMock.mock.calls).toEqual([
      [
        `/backup-repositories/${repositoryId}/recovery-points?limit=25&cursor=point-cursor&sort=captured_asc`,
        { token: "token", signal },
      ],
      [`/recovery-points/${recoveryPointId}`, { token: "token", signal }],
      [`/recovery-points/${recoveryPointId}/catalog-status`, { token: "token", signal }],
      [`/recovery-points/${recoveryPointId}/evidence`, { token: "token", signal }],
    ]);
    expect(JSON.stringify(requestMock.mock.calls)).not.toMatch(/path|locator|native_id/i);
  });
});
