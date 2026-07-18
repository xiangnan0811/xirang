import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import {
  createBackupAssetsApi,
  mapBackupAsset,
  mapRecoveryPointDiff,
} from "./backup-assets-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);
const baseRecoveryPointId = "1".repeat(32);
const compareRecoveryPointId = "2".repeat(32);
const baseEntryId = "a".repeat(64);
const compareEntryId = "b".repeat(64);
const parentEntryId = "c".repeat(64);

function rawEntry() {
  return {
    recovery_point_id: baseRecoveryPointId,
    entry_id: baseEntryId,
    parent_entry_id: parentEntryId,
    name: "database.dump",
    entry_type: "file",
    size: 42,
    modified_at: "2026-07-18T08:00:00+08:00",
    mode: "0640",
    owner: "backup",
    mime_type: "application/octet-stream",
    fingerprint_strength: "strong",
    breadcrumb: [
      {
        recovery_point_id: baseRecoveryPointId,
        entry_id: parentEntryId,
        name: "exports",
        normalized_path: "/PRIVATE/exports",
      },
    ],
    fingerprint: "PRIVATE RAW FINGERPRINT",
    normalized_path: "/PRIVATE/database.dump",
    encrypted_provider_locator: "PRIVATE LOCATOR",
  };
}

function rawDiff() {
  return {
    items: [
      {
        kind: "modified",
        base: {
          recovery_point_id: baseRecoveryPointId,
          entry_id: baseEntryId,
          name: "database.dump",
          entry_type: "file",
          size: 42,
          modified_at: "2026-07-18T00:00:00Z",
          mode: "0640",
          owner: "backup",
          mime_type: "application/octet-stream",
          fingerprint_strength: "strong",
          fingerprint: "PRIVATE BASE FINGERPRINT",
        },
        compare: {
          recovery_point_id: compareRecoveryPointId,
          entry_id: compareEntryId,
          name: "database.dump",
          entry_type: "file",
          size: 84,
          modified_at: null,
          mode: "0640",
          owner: "backup",
          mime_type: "application/octet-stream",
          fingerprint_strength: "strong",
          fingerprint: "PRIVATE COMPARE FINGERPRINT",
        },
        changed_fields: ["size", "modified_at", "content"],
        content_equality: "different",
        normalized_path: "/PRIVATE/database.dump",
      },
    ],
    next_cursor: "next-diff",
    provider_evidence: {
      status: "unavailable",
      reason: {
        code: "provider_unavailable",
        params: { retry_after_seconds: "30" },
        raw_message: "PRIVATE PROVIDER MESSAGE",
      },
    },
  };
}

describe("backup assets API boundary", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("maps entries and breadcrumbs to composite AssetRef values", () => {
    const mapped = mapBackupAsset(rawEntry());

    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") {
      throw new Error("expected available asset");
    }
    expect(mapped.value).toMatchObject({
      ref: { recoveryPointId: baseRecoveryPointId, entryId: baseEntryId },
      parentRef: { recoveryPointId: baseRecoveryPointId, entryId: parentEntryId },
      name: "database.dump",
      entryType: "file",
      size: 42,
      modifiedAt: "2026-07-18T00:00:00.000Z",
      fingerprintStrength: "strong",
      breadcrumb: [
        {
          ref: { recoveryPointId: baseRecoveryPointId, entryId: parentEntryId },
          name: "exports",
        },
      ],
    });
    const serialized = JSON.stringify(mapped);
    for (const forbidden of ["PRIVATE", "\"fingerprint\":", "normalized_path", "provider_locator"]) {
      expect(serialized).not.toContain(forbidden);
    }
  });

  it("blocks the whole entry projection for an unknown closed enum", () => {
    const mapped = mapBackupAsset({ ...rawEntry(), entry_type: "future_type" });
    expect(mapped).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(JSON.stringify(mapped)).not.toContain("future_type");
  });

  it("maps both diff sides to distinct composite refs and drops raw fingerprints", () => {
    const mapped = mapRecoveryPointDiff(rawDiff());

    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") {
      throw new Error("expected available diff");
    }
    expect(mapped.value).toMatchObject({
      nextCursor: "next-diff",
      items: [
        {
          kind: "modified",
          base: { ref: { recoveryPointId: baseRecoveryPointId, entryId: baseEntryId } },
          compare: { ref: { recoveryPointId: compareRecoveryPointId, entryId: compareEntryId } },
          changedFields: ["size", "modified_at", "content"],
          contentEquality: "different",
        },
      ],
      providerEvidence: {
        status: "unavailable",
        reason: { code: "provider_unavailable", params: { retry_after_seconds: "30" } },
      },
    });
    const serialized = JSON.stringify(mapped);
    expect(serialized).not.toContain("PRIVATE");
    expect(serialized).not.toContain("fingerprint\"");
    expect(serialized).not.toContain("normalized_path");
  });

  it("sends exact list/detail/diff shapes using opaque refs and AbortSignal", async () => {
    const signal = new AbortController().signal;
    requestMock
      .mockResolvedValueOnce({ items: [rawEntry()], next_cursor: "next-entry" })
      .mockResolvedValueOnce(rawEntry())
      .mockResolvedValueOnce(rawDiff());

    const api = createBackupAssetsApi();
    const parentRef = { recoveryPointId: baseRecoveryPointId, entryId: parentEntryId };
    await api.listBackupAssets("token", baseRecoveryPointId, {
      parent: parentRef,
      limit: 100,
      cursor: "entry-cursor",
      sort: "modified_desc",
      signal,
    });
    await api.getBackupAsset(
      "token",
      { recoveryPointId: baseRecoveryPointId, entryId: baseEntryId },
      signal,
    );
    await api.diffRecoveryPoints(
      "token",
      {
        baseRecoveryPointId,
        compareRecoveryPointId,
        baseParent: parentRef,
        compareParent: { recoveryPointId: compareRecoveryPointId, entryId: compareEntryId },
        sort: "path_asc",
        limit: 75,
        cursor: "diff-cursor",
      },
      signal,
    );

    expect(requestMock).toHaveBeenNthCalledWith(
      1,
      `/recovery-points/${baseRecoveryPointId}/entries?parent=${parentEntryId}&limit=100&cursor=entry-cursor&sort=modified_desc`,
      { token: "token", signal },
    );
    expect(requestMock).toHaveBeenNthCalledWith(
      2,
      `/recovery-points/${baseRecoveryPointId}/entries/${baseEntryId}`,
      { token: "token", signal },
    );
    expect(requestMock).toHaveBeenNthCalledWith(
      3,
      "/recovery-point-diffs",
      {
        method: "POST",
        token: "token",
        signal,
        body: {
          base_recovery_point_id: baseRecoveryPointId,
          compare_recovery_point_id: compareRecoveryPointId,
          base_parent_entry_id: parentEntryId,
          compare_parent_entry_id: compareEntryId,
          sort: "path_asc",
          limit: 75,
          cursor: "diff-cursor",
        },
      },
    );
    const serializedCalls = JSON.stringify(requestMock.mock.calls);
    expect(serializedCalls).not.toMatch(/normalized_path|provider_locator|native_id/i);
  });
});
