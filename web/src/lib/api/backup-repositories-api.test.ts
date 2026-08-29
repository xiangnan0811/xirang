import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import {
  createBackupRepositoriesApi,
  mapBackupImportCandidate,
  mapBackupImportDiscoveryResult,
  mapBackupRebuildResult,
  mapBackupRepository,
  mapBackupRepositoryMutationResult,
} from "./backup-repositories-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);

const repositoryId = "1".repeat(32);

function rawRepository() {
  return {
    id: repositoryId,
    provider_kind: "restic",
    display_name: "Production backups",
    description: "Primary immutable repository",
    version_mode: "native_snapshot",
    status: "offline",
    capability_revision: 7,
    capabilities: {
      list: true,
      search_path: false,
      open_sequential: false,
      open_range: false,
      download: false,
      restore: false,
      diff: true,
      native_history: true,
      reason: {
        code: "repository_offline",
        params: { retry_after_seconds: "30" },
        provider_message: "PRIVATE PROVIDER MESSAGE",
      },
    },
    immutability_level: "backend_versioned",
    last_seen_at: "2026-07-18T08:00:00+08:00",
    last_reconciled_at: null,
    created_at: "2026-07-18T00:00:00Z",
    updated_at: "2026-07-18T01:00:00Z",
    access_active: true,
    lineages: [
      {
        source: "task_link",
        task_id: 9,
        task_name: "nightly",
        node_id: 7,
        node_name: "db-a",
        publication_mode: "native_snapshot",
        active: true,
        provider_locator: "/PRIVATE/REPOSITORY/PATH",
      },
    ],
    catalog: {
      recovery_point_count: 3,
      complete_catalog_count: 2,
      coverage: "partial",
      content_availability: {
        available: false,
        reason: { code: "repository_offline", params: {} },
      },
      permissions: { list: true, preview: false, download: false },
    },
    encrypted_access_config: "PRIVATE SECRET",
    repository_identity: "PRIVATE IDENTITY",
  };
}

function rawMutablePoint() {
  return {
    id: "2".repeat(32),
    repository_id: repositoryId,
    lineage: { producing_task_id: 42 },
    semantics: "mutable_head",
    state: "observed",
    physical_availability: "online",
    hold_state: "none",
    immutability_level: "mutable",
    manifest_digest: "",
    entry_count: 0,
    logical_bytes: 0,
    captured_at: null,
    committed_at: null,
    observed_at: "2026-08-17T00:00:00Z",
    capability_revision: 1,
    capabilities: {
      list: true,
      search_path: true,
      open_sequential: true,
      open_range: true,
      download: true,
      restore: true,
      diff: false,
      native_history: false,
      reason: null,
    },
    created_at: "2026-08-17T00:00:00Z",
    updated_at: "2026-08-17T00:00:00Z",
    encrypted_provider_locator: "PRIVATE LOCATOR",
  };
}

describe("backup repositories API boundary", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("maps a repository to camelCase, normalizes UTC, and drops private fields", () => {
    const mapped = mapBackupRepository(rawRepository());

    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") {
      throw new Error("expected available repository projection");
    }
    expect(mapped.value).toMatchObject({
      id: repositoryId,
      providerKind: "restic",
      displayName: "Production backups",
      versionMode: "native_snapshot",
      status: "offline",
      immutabilityLevel: "backend_versioned",
      lastSeenAt: "2026-07-18T00:00:00.000Z",
      lastReconciledAt: null,
      catalog: {
        recoveryPointCount: 3,
        completeCatalogCount: 2,
        coverage: "partial",
        contentAvailability: {
          available: false,
          reason: { code: "repository_offline", params: {} },
        },
      },
      lineages: [
        {
          source: "task_link",
          taskId: 9,
          taskName: "nightly",
          nodeId: 7,
          nodeName: "db-a",
          publicationMode: "native_snapshot",
          active: true,
        },
      ],
    });
    expect(mapped.value.capabilities.reason).toEqual({
      code: "repository_offline",
      params: { retry_after_seconds: "30" },
    });

    const serialized = JSON.stringify(mapped);
    for (const forbidden of ["PRIVATE", "provider_locator", "encrypted_access_config", "repository_identity", "provider_message"]) {
      expect(serialized).not.toContain(forbidden);
    }
  });

  it("projects an unknown closed enum as one blocked object without echoing the raw value", () => {
    const mapped = mapBackupRepository({
      ...rawRepository(),
      provider_kind: "future_private_provider",
    });

    expect(mapped).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(JSON.stringify(mapped)).not.toContain("future_private_provider");
  });

  it("uses a closed capability-reason fallback without exposing raw params", () => {
    const raw = rawRepository();
    const mapped = mapBackupRepository({
      ...raw,
      capabilities: {
        ...raw.capabilities,
        reason: {
          code: "future_reason",
          params: { raw_provider_error: "PRIVATE ERROR" },
          provider_message: "PRIVATE PROVIDER MESSAGE",
        },
      },
    });
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") {
      throw new Error("expected available repository projection");
    }
    expect(mapped.value.capabilities.reason).toEqual({
      code: "unknown_internal_state",
      params: {},
    });
    expect(JSON.stringify(mapped)).not.toContain("PRIVATE");
  });

  it("sends exact list/detail query shapes and forwards AbortSignal", async () => {
    const signal = new AbortController().signal;
    requestMock
      .mockResolvedValueOnce({ items: [rawRepository()], next_cursor: "next-repository" })
      .mockResolvedValueOnce(rawRepository());

    const api = createBackupRepositoriesApi();
    const page = await api.listBackupRepositories("token", {
      limit: 50,
      cursor: "cursor-value",
      signal,
    });
    const detail = await api.getBackupRepository("token", repositoryId, signal);

    expect(requestMock).toHaveBeenNthCalledWith(
      1,
      "/backup-repositories?limit=50&cursor=cursor-value",
      { token: "token", signal },
    );
    expect(requestMock).toHaveBeenNthCalledWith(
      2,
      `/backup-repositories/${repositoryId}`,
      { token: "token", signal },
    );
    expect(page.nextCursor).toBe("next-repository");
    expect(page.items[0].status).toBe("available");
    expect(detail.status).toBe("available");
    expect(JSON.stringify(requestMock.mock.calls)).not.toMatch(/path|locator|native/i);
  });

  it("maps connect/import/rebuild results and drops private locator fields", () => {
    const connect = mapBackupRepositoryMutationResult({
      Repository: {
        id: repositoryId,
        provider_kind: "restic",
        display_name: "Imported",
        description: "",
        version_mode: "native_snapshot",
        status: "disconnected",
        capability_revision: 1,
        capabilities: {
          list: false,
          search_path: false,
          open_sequential: false,
          open_range: false,
          download: false,
          restore: false,
          diff: false,
          native_history: false,
          reason: { code: "repository_offline", params: {} },
        },
        immutability_level: "mutable",
        created_at: "2026-08-17T00:00:00Z",
        updated_at: "2026-08-17T00:00:00Z",
        repository_identity: "PRIVATE IDENTITY",
      },
    });
    const candidate = mapBackupImportCandidate({
      id: "2".repeat(32),
      repository_id: repositoryId,
      kind: "imported_baseline",
      state: "pending",
      created_at: "2026-08-17T00:00:00Z",
      provider_locator: "/PRIVATE/CANDIDATE",
    });
    const discovery = mapBackupImportDiscoveryResult({
      candidates: [{
        id: "2".repeat(32),
        repository_id: repositoryId,
        kind: "imported_baseline",
        state: "pending",
        created_at: "2026-08-17T00:00:00Z",
      }],
      next_cursor: "next-import",
      discovered: 1,
      existing: 0,
    });
    const rebuild = mapBackupRebuildResult({
      accepted: 1,
      catalog_started: 1,
      derived_queued: 0,
      partial: 0,
      failed: 0,
      reasons: { invalid_manifest: 0 },
      next_cursor: "",
    });

    expect(connect.status).toBe("available");
    if (connect.status !== "available") {
      throw new Error("expected available connect snapshot");
    }
    expect(connect.value.repository).toMatchObject({
      id: repositoryId,
      providerKind: "restic",
      status: "disconnected",
    });
    expect(connect.value.repository).not.toHaveProperty("accessActive");
    expect(connect.value.repository).not.toHaveProperty("catalog");
    expect(connect.value.repository).not.toHaveProperty("lineages");
    expect(candidate).toMatchObject({
      status: "available",
      value: { kind: "imported_baseline", state: "pending", repositoryId },
    });
    expect(discovery).toMatchObject({
      status: "available",
      value: { discovered: 1, existing: 0, nextCursor: "next-import" },
    });
    expect(rebuild).toMatchObject({
      status: "available",
      value: { accepted: 1, catalogStarted: 1, reasons: { invalid_manifest: 0 } },
    });
    expect(JSON.stringify([connect, candidate, discovery, rebuild])).not.toContain("PRIVATE");
    expect(JSON.stringify(mapBackupRebuildResult({
      accepted: 0,
      catalog_started: 0,
      derived_queued: 0,
      partial: 0,
      failed: 1,
      reasons: { future_private_reason: 1 },
    }))).not.toContain("future_private_reason");
    expect(mapBackupImportCandidate({
      id: "2".repeat(32),
      repository_id: repositoryId,
      kind: "imported_baseline",
      state: "accepted",
      created_at: "2026-08-17T00:00:00Z",
    })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(mapBackupImportCandidate({
      id: "2".repeat(32),
      repository_id: repositoryId,
      kind: "imported_baseline",
      state: "pending",
      accepted_recovery_point_id: "3".repeat(32),
      created_at: "2026-08-17T00:00:00Z",
    })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("maps PascalCase and snake_case mutable-point snapshots and fails closed when present but malformed", () => {
    for (const raw of [
      { Repository: rawRepository(), MutablePoint: rawMutablePoint() },
      { repository: rawRepository(), mutable_point: rawMutablePoint() },
    ]) {
      const mapped = mapBackupRepositoryMutationResult(raw);
      expect(mapped.status).toBe("available");
      if (mapped.status !== "available") {
        throw new Error("expected available mutation result");
      }
      expect(mapped.value.mutablePoint).toMatchObject({
        id: "2".repeat(32),
        repositoryId,
        semantics: "mutable_head",
        state: "observed",
      });
      expect(JSON.stringify(mapped)).not.toMatch(/mutable_point|MutablePoint|PRIVATE|provider_locator/);
    }

    for (const raw of [
      { repository: rawRepository() },
      { repository: rawRepository(), mutable_point: null },
      { Repository: rawRepository(), MutablePoint: null },
    ]) {
      const mapped = mapBackupRepositoryMutationResult(raw);
      expect(mapped.status).toBe("available");
      if (mapped.status === "available") {
        expect(mapped.value.mutablePoint).toBeNull();
      }
    }

    expect(mapBackupRepositoryMutationResult({
      repository: rawRepository(),
      mutable_point: { ...rawMutablePoint(), observed_at: "PRIVATE INVALID TIME" },
    })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("fails closed for duplicate, conflicting, or mixed mutation envelopes", () => {
    const blockedResult = {
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    };
    for (const raw of [
      { Repository: rawRepository(), repository: rawRepository() },
      {
        Repository: rawRepository(),
        repository: { ...rawRepository(), id: "9".repeat(32) },
      },
      {
        Repository: rawRepository(),
        MutablePoint: rawMutablePoint(),
        mutable_point: rawMutablePoint(),
      },
      { Repository: rawRepository(), mutable_point: rawMutablePoint() },
      { repository: rawRepository(), MutablePoint: rawMutablePoint() },
    ]) {
      expect(mapBackupRepositoryMutationResult(raw)).toEqual(blockedResult);
    }
  });

  it("sends a task-only connect body for preview association", async () => {
    requestMock.mockResolvedValueOnce({ repository: rawRepository(), mutable_point: rawMutablePoint() });

    await createBackupRepositoriesApi().connectBackupRepository("token", { taskId: 42 });

    expect(requestMock).toHaveBeenCalledWith("/backup-repositories/connect", {
      method: "POST",
      token: "token",
      signal: undefined,
      body: { task_id: 42 },
    });
  });

  it("maps import candidate quarantined fail-closed", () => {
    const base = {
      id: "2".repeat(32),
      repository_id: repositoryId,
      kind: "imported_baseline",
      state: "pending",
      created_at: "2026-08-17T00:00:00Z",
    };
    expect(mapBackupImportCandidate({ ...base, quarantined: true })).toMatchObject({
      status: "available",
      value: { quarantined: true },
    });
    expect(mapBackupImportCandidate({ ...base, quarantined: false })).toMatchObject({
      status: "available",
      value: { quarantined: false },
    });
    expect(mapBackupImportCandidate(base)).toMatchObject({
      status: "available",
      value: { quarantined: false },
    });
    expect(mapBackupImportCandidate({ ...base, quarantined: "yes" })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(mapBackupImportCandidate({ ...base, quarantined: 1 })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("sends exact connect, import, review, and rebuild request shapes", async () => {
    const signal = new AbortController().signal;
    requestMock
      .mockResolvedValueOnce({
        repository: {
          ...rawRepository(),
          lineages: undefined,
          catalog: undefined,
        },
      })
      .mockResolvedValueOnce({
        candidates: [],
        discovered: 0,
        existing: 0,
      })
      .mockResolvedValueOnce({ items: [], next_cursor: "" })
      .mockResolvedValueOnce({
        id: "2".repeat(32),
        repository_id: repositoryId,
        kind: "imported_baseline",
        state: "accepted",
        accepted_recovery_point_id: "3".repeat(32),
        created_at: "2026-08-17T00:00:00Z",
        reviewed_at: "2026-08-17T01:00:00Z",
      })
      .mockResolvedValueOnce({
        accepted: 1,
        catalog_started: 1,
        derived_queued: 0,
        partial: 0,
        failed: 0,
        reasons: {},
      });

    const api = createBackupRepositoriesApi();
    await api.connectBackupRepository("token", {
      taskId: 42,
      repositoryId,
      displayName: "Restored",
      replaceAccess: true,
    }, signal);
    await api.scanBackupRepositoryImports("token", repositoryId, { limit: 25, cursor: "opaque-cursor", signal });
    await api.listBackupRepositoryImportCandidates("token", repositoryId, { limit: 10, cursor: "opaque-cursor", signal });
    await api.reviewBackupRepositoryImportCandidate("token", repositoryId, "2".repeat(32), {
      decision: "accepted",
      acceptAs: "imported_baseline",
    }, signal);
    await api.rebuildBackupRepositoryImports("token", repositoryId, { limit: 5, signal });

    expect(requestMock).toHaveBeenNthCalledWith(1, "/backup-repositories/connect", {
      method: "POST",
      token: "token",
      signal,
      body: {
        task_id: 42,
        repository_id: repositoryId,
        display_name: "Restored",
        replace_access: true,
      },
    });
    expect(requestMock).toHaveBeenNthCalledWith(2, `/backup-repositories/${repositoryId}/import-scans`, {
      method: "POST",
      token: "token",
      signal,
      body: { limit: 25, cursor: "opaque-cursor" },
    });
    expect(requestMock).toHaveBeenNthCalledWith(
      3,
      `/backup-repositories/${repositoryId}/import-candidates?limit=10&cursor=opaque-cursor`,
      { token: "token", signal },
    );
    expect(requestMock).toHaveBeenNthCalledWith(
      4,
      `/backup-repositories/${repositoryId}/import-candidates/${"2".repeat(32)}/reviews`,
      {
        method: "POST",
        token: "token",
        signal,
        body: { decision: "accepted", accept_as: "imported_baseline" },
      },
    );
    expect(requestMock).toHaveBeenNthCalledWith(5, `/backup-repositories/${repositoryId}/rebuilds`, {
      method: "POST",
      token: "token",
      signal,
      body: { limit: 5 },
    });
  });

  it("posts empty-body reconcile and disconnect mutations", async () => {
    const signal = new AbortController().signal;
    requestMock
      .mockResolvedValueOnce({ repository: rawRepository() })
      .mockResolvedValueOnce({ repository: { ...rawRepository(), status: "disconnected", access_active: false } });

    const api = createBackupRepositoriesApi();
    await api.reconcileBackupRepository("token", repositoryId, signal);
    await api.disconnectBackupRepository("token", repositoryId, signal);

    expect(requestMock).toHaveBeenNthCalledWith(1, `/backup-repositories/${repositoryId}/reconcile`, {
      method: "POST",
      token: "token",
      signal,
    });
    expect(requestMock).toHaveBeenNthCalledWith(2, `/backup-repositories/${repositoryId}/disconnect`, {
      method: "POST",
      token: "token",
      signal,
    });
  });
});
