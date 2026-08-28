import { beforeEach, describe, expect, it, vi } from "vitest";

import { request } from "./core";
import {
  createBackupFileSourcesApi,
  mapBackupFileSourceNodePage,
  mapBackupFileSourceRecoveryPoint,
  mapBackupFileSourceSetPage,
  mapBackupFileSourceVersionPage,
} from "./backup-file-sources-api";

vi.mock("./core", async () => ({
  ...(await vi.importActual<typeof import("./core")>("./core")),
  request: vi.fn(),
}));

const requestMock = vi.mocked(request);
const setId = "a".repeat(32);
const pointId = "b".repeat(32);
const repositoryId = "c".repeat(32);
const cursor = "eyJvZmZzZXQiOjEwfQ.signature_1";

const rawNode = {
  node_id: 7,
  display_name: "数据库节点",
  backup_set_count: 2,
  retained_version_count: 3,
  latest_retained_at: "2026-08-27T00:00:00Z",
  catalog_coverage: "partial",
  browse_state: "browsable",
};

const rawSet = {
  backup_set_id: setId,
  node_id: 7,
  display_label: "每日备份",
  lineage_kind: "task",
  version_count: 3,
  latest_retained_at: null,
  catalog_coverage: "complete",
  browse_state: "browsable",
};

const rawVersion = {
  recovery_point_id: pointId,
  repository_id: repositoryId,
  producing_task_id: 9,
  captured_at: "2026-08-27T00:00:00Z",
  committed_at: "2026-08-27T00:01:00Z",
  created_at: "2026-08-27T00:00:30Z",
  lifecycle_state: "committed",
  catalog_coverage: "complete",
  content_availability: { available: true, reason: null },
  entry_count: 12,
  logical_bytes: 4096,
  permissions: { list: true, preview: false, download: false },
  browse_state: "browsable",
};

const rawResolution = {
  node_id: 7,
  backup_set_id: setId,
  recovery_point_id: pointId,
  repository_id: repositoryId,
  producing_task_id: 9,
  browse_state: "browsable",
};

describe("backup file source API boundary", () => {
  beforeEach(() => requestMock.mockReset());

  it("strictly maps complete node, set, and version pages without leaking extra fields", () => {
    expect(mapBackupFileSourceNodePage({ items: [{ ...rawNode, private_path: "/secret" }], next_cursor: cursor }))
      .toMatchObject({ status: "available", value: { items: [{ nodeId: 7, displayName: "数据库节点" }], nextCursor: cursor } });
    expect(mapBackupFileSourceSetPage({ items: [rawSet] })).toMatchObject({
      status: "available",
      value: { items: [{ backupSetId: setId, lineageKind: "task" }], nextCursor: null },
    });
    expect(mapBackupFileSourceVersionPage({ items: [{ ...rawVersion, raw_locator: "PRIVATE" }] })).toMatchObject({
      status: "available",
      value: { items: [{ recoveryPointId: pointId, repositoryId, producingTaskId: 9, browseState: "browsable" }] },
    });
    expect(JSON.stringify(mapBackupFileSourceVersionPage({ items: [{ ...rawVersion, raw_locator: "PRIVATE" }] })))
      .not.toContain("PRIVATE");
  });

  it("strictly maps the exact recovery-point source without retaining private extras", () => {
    const mapped = mapBackupFileSourceRecoveryPoint({
      ...rawResolution,
      provider_locator: "PRIVATE_LOCATOR",
      normalized_path: "/private/path",
      content: "PRIVATE_CONTENT",
      proof: "PRIVATE_PROOF",
    });

    expect(mapped).toEqual({
      status: "available",
      value: {
        nodeId: 7,
        backupSetId: setId,
        recoveryPointId: pointId,
        repositoryId,
        producingTaskId: 9,
        browseState: "browsable",
        unavailableReason: null,
      },
    });
    expect(JSON.stringify(mapped)).not.toMatch(/PRIVATE|locator|path|content|proof/i);
  });

  it("preserves an omitted optional producing task for taskless lineage", () => {
    const { producing_task_id: _producingTaskId, ...tasklessResolution } = rawResolution;

    expect(mapBackupFileSourceRecoveryPoint(tasklessResolution)).toEqual({
      status: "available",
      value: {
        nodeId: 7,
        backupSetId: setId,
        recoveryPointId: pointId,
        repositoryId,
        producingTaskId: undefined,
        browseState: "browsable",
        unavailableReason: null,
      },
    });
  });

  it.each([
    { ...rawResolution, node_id: 0 },
    { ...rawResolution, backup_set_id: "latest" },
    { ...rawResolution, recovery_point_id: "latest" },
    { ...rawResolution, repository_id: "latest" },
    { ...rawResolution, producing_task_id: 0 },
    { ...rawResolution, producing_task_id: null },
  ])("blocks a malformed exact recovery-point source DTO", (value) => {
    expect(mapBackupFileSourceRecoveryPoint(value)).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it.each([
    { page: "node", value: { items: [{ ...rawNode, node_id: 0 }] } },
    { page: "node", value: { items: [{ ...rawNode, backup_set_count: 0 }] } },
    { page: "node", value: { items: [{ ...rawNode, latest_retained_at: "not-time" }] } },
    { page: "node", value: { items: [{ ...rawNode, latest_retained_at: "2026-08-27T08:00:00+08:00" }] } },
    { page: "set", value: { items: [{ ...rawSet, backup_set_id: "partial" }] } },
    { page: "set", value: { items: [{ ...rawSet, lineage_kind: "future" }] } },
    { page: "version", value: { items: [{ ...rawVersion, lifecycle_state: "future" }] } },
    { page: "version", value: { items: [{ ...rawVersion, permissions: { list: true, preview: true } }] } },
    { page: "version", value: { items: [{ ...rawVersion, captured_at: "not-time" }] } },
    { page: "version", value: { items: [{ ...rawVersion, producing_task_id: 0 }] } },
    { page: "version", value: { items: [{ ...rawVersion, browse_state: "future" }] } },
    { page: "version", value: { items: [{ ...rawVersion, browse_state: "unavailable" }] } },
    { page: "version", value: { items: [{ ...rawVersion, content_availability: { available: false, reason: { code: "provider_unavailable", params: {} } } }] } },
    { page: "version", value: { items: [{ ...rawVersion, unavailable_reason: { code: "repository_offline", params: { repository_status: "SECRET" } } }] } },
  ])("blocks the complete $page page for malformed or partial items", ({ page, value }) => {
    const mapped = page === "node"
      ? mapBackupFileSourceNodePage(value)
      : page === "set"
        ? mapBackupFileSourceSetPage(value)
        : mapBackupFileSourceVersionPage(value);
    expect(mapped).toEqual({ status: "blocked", reason: { code: "unknown_internal_state", params: {} } });
  });

  it("maps retained indexing and parameter-free unavailable states without internal facts", () => {
    expect(mapBackupFileSourceVersionPage({ items: [{
      ...rawVersion,
      lifecycle_state: "verifying",
      catalog_coverage: "building",
      browse_state: "indexing",
    }] })).toMatchObject({ status: "available", value: { items: [{ browseState: "indexing", unavailableReason: null }] } });
    expect(mapBackupFileSourceSetPage({ items: [{
      ...rawSet,
      browse_state: "unavailable",
      unavailable_reason: { code: "repository_offline" },
    }] })).toMatchObject({
      status: "available",
      value: { items: [{ browseState: "unavailable", unavailableReason: { code: "repository_offline", params: {} } }] },
    });
  });

  it("blocks malformed and oversized cursors instead of making them reusable", () => {
    expect(mapBackupFileSourceNodePage({ items: [], next_cursor: "raw/path?token=secret" }).status).toBe("blocked");
    expect(mapBackupFileSourceNodePage({ items: [], next_cursor: `${"a".repeat(8192)}.b` }).status).toBe("blocked");
  });

  it("preserves valid UTC sub-millisecond precision for deterministic version ordering", () => {
    const mapped = mapBackupFileSourceVersionPage({
      items: [{ ...rawVersion, captured_at: "2026-08-27T00:00:00.123456789Z" }],
    });

    expect(mapped).toMatchObject({
      status: "available",
      value: { items: [{ capturedAt: "2026-08-27T00:00:00.123456789Z" }] },
    });
  });

  it("blocks an entire page when an opaque identity is duplicated", () => {
    expect(mapBackupFileSourceSetPage({ items: [rawSet, rawSet] })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("blocks a set page whose node identity does not match the requested hierarchy", async () => {
    requestMock.mockResolvedValue({ items: [{ ...rawSet, node_id: 8 }] });

    await expect(createBackupFileSourcesApi().listBackupFileSourceSets("token", 7)).resolves.toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });

  it("uses the central request wrapper with safe cursor paging and abort signals", async () => {
    requestMock.mockResolvedValue({ items: [rawNode], next_cursor: cursor });
    const signal = new AbortController().signal;
    const result = await createBackupFileSourcesApi().listBackupFileSourceNodes("token", {
      limit: 50,
      cursor,
      signal,
    });
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-file-sources/nodes?limit=50&cursor=${encodeURIComponent(cursor)}`,
      { token: "token", signal },
    );
    expect(result.status).toBe("available");
  });

  it("resolves one canonical recovery point through the central request wrapper and AbortSignal", async () => {
    requestMock.mockResolvedValue(rawResolution);
    const signal = new AbortController().signal;

    await expect(createBackupFileSourcesApi().resolveBackupFileSourceRecoveryPoint("token", pointId, { signal }))
      .resolves.toMatchObject({ status: "available", value: { nodeId: 7, backupSetId: setId } });
    expect(requestMock).toHaveBeenCalledTimes(1);
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-file-sources/recovery-points/${pointId}/source`,
      { token: "token", signal },
    );
  });

  it("blocks a response for a different recovery point and rejects non-canonical input before requesting", async () => {
    requestMock.mockResolvedValue({ ...rawResolution, recovery_point_id: "d".repeat(32) });
    await expect(createBackupFileSourcesApi().resolveBackupFileSourceRecoveryPoint("token", pointId))
      .resolves.toEqual({ status: "blocked", reason: { code: "unknown_internal_state", params: {} } });

    requestMock.mockClear();
    await expect(createBackupFileSourcesApi().resolveBackupFileSourceRecoveryPoint("token", "latest"))
      .rejects.toThrow("invalid recovery point id");
    expect(requestMock).not.toHaveBeenCalled();
  });
});
