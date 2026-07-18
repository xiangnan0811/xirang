import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import {
  createBackupRepositoriesApi,
  mapBackupRepository,
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
});
