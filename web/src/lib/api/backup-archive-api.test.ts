import { describe, expect, it, vi } from "vitest";

import {
  createBackupArchiveApi,
  mapBackupArchiveIndex,
  mapBackupArchiveStatus,
} from "./backup-archive-api";
import type { BackupArchiveFailureProduct, BackupArchiveMemberState } from "@/types/domain";

const recoveryPointId = "1".repeat(32);
const entryId = "2".repeat(64);
const requestId = "3".repeat(32);
const memberId = "4".repeat(32);
const indexRevision = "5".repeat(64);
const assetRef = { recovery_point_id: recoveryPointId, entry_id: entryId };

function rawIndex() {
  return {
    schema_version: 1,
    index_revision: indexRevision,
    expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
    entries: [
      {
        id: memberId,
        display_name: "member.txt",
        type: "file",
        size: 12,
        media_type: "text/plain",
        warning: "none",
      },
    ],
  };
}

function rawIndexWithEntries(count: number) {
  return {
    ...rawIndex(),
    entries: Array.from({ length: count }, (_, index) => {
      const id = (index + 1).toString(16).padStart(32, "0");
      const parentId = index === 0 ? null : index.toString(16).padStart(32, "0");
      return {
        id,
        ...(parentId === null ? {} : { parent_id: parentId }),
        display_name: `member-${index}`,
        type: "file",
        size: 0,
        media_type: "application/octet-stream",
        warning: "none",
      };
    }),
  };
}

function rawStatus(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: 1,
    request_id: requestId,
    asset_ref: assetRef,
    index_revision: indexRevision,
    state: "failed",
    failure_product: "limit",
    fallback: { action: "download_original" },
    retryable: false,
    terminal: true,
    ...overrides,
  };
}

function rawStatusForState(state: BackupArchiveMemberState) {
  const failed = state === "failed";
  return {
    schema_version: 1,
    request_id: requestId,
    asset_ref: assetRef,
    index_revision: indexRevision,
    state,
    ...(failed ? { failure_product: "limit" } : {}),
    fallback: failed ? { action: "download_original" } : {},
    retryable: false,
    terminal: state === "ready" || failed || state === "canceled" || state === "expired",
  };
}

function rawCreate(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: 1,
    request_id: requestId,
    asset_ref: assetRef,
    index_revision: indexRevision,
    state: "queued",
    ...overrides,
  };
}

function rawTicket(contentUrl = `/api/v1/asset-content/${requestId}`) {
  return {
    schema_version: 1,
    content_url: contentUrl,
    content_type: "text/plain",
    content_length: 12,
    etag: '"member-etag"',
    range: "none",
    expires_at: new Date(Date.now() + 60_000).toISOString(),
    idle_expires_at: new Date(Date.now() + 30_000).toISOString(),
  };
}

describe("backup archive API boundary", () => {
  it("maps an index and folds a persisted input-too-large result into closed limit", () => {
    expect(mapBackupArchiveIndex(rawIndex())).toMatchObject({
      schemaVersion: 1,
      indexRevision: "5".repeat(64),
      entries: [{ id: memberId, displayName: "member.txt", type: "file", size: 12, mediaType: "text/plain", warning: "none" }],
    });
    expect(mapBackupArchiveStatus(rawStatus())).toMatchObject({
      schemaVersion: 1,
      requestId,
      state: "failed",
      failureProduct: "limit",
      fallback: { action: "download_original", reason: null },
    });
  });

  it("rejects a human-form index timestamp that Date.parse accepts", () => {
    const raw = rawIndex();

    expect(() => mapBackupArchiveIndex({
      ...raw,
      expires_at: raw.expires_at.replace("T", " "),
    })).toThrow("invalid backup archive response");
  });

  it("rejects an impossible RFC3339 calendar date rather than normalizing it", () => {
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      expires_at: "2999-02-30T00:00:00Z",
    })).toThrow("invalid backup archive response");
  });

  it.each([
    "2999-01-01T24:00:00Z",
    "2999-01-01T00:00:00+24:00",
    "2999-01-01T00:00:00+23:60",
    "2999-01-01T00:00:00-00:00",
  ])("rejects an out-of-range RFC3339 clock or offset: %s", (expiresAt) => {
    expect(() => mapBackupArchiveIndex({ ...rawIndex(), expires_at: expiresAt }))
      .toThrow("invalid backup archive response");
  });

  it("rejects an RFC3339 instant whose UTC normalization exceeds the four-digit year range", () => {
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      expires_at: "9999-12-31T23:59:59-00:01",
    }))
      .toThrow("invalid backup archive response");
  });

  it("rejects an RFC3339 instant whose UTC normalization underflows the four-digit year range", () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date(-62_167_219_320_000));

      expect(() => mapBackupArchiveIndex({
        ...rawIndex(),
        expires_at: "0000-01-01T00:00:00+00:01",
      })).toThrow("invalid backup archive response");
    } finally {
      vi.useRealTimers();
    }
  });

  it("rejects an expired archive index", () => {
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      expires_at: new Date(Date.now() - 1_000).toISOString(),
    })).toThrow("invalid backup archive response");
  });

  it("canonicalizes an archive index expiry without losing nanoseconds across offsets", () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date("2026-12-31T15:59:59.999Z"));

      expect(mapBackupArchiveIndex({
        ...rawIndex(),
        expires_at: "2027-01-01T08:00:00.000123456+08:00",
      }).expiresAt).toBe("2027-01-01T00:00:00.000123456Z");
    } finally {
      vi.useRealTimers();
    }
  });

  it("accepts an archive index expiry one nanosecond after the browser clock", () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date("2027-01-01T00:00:00.000Z"));

      expect(mapBackupArchiveIndex({
        ...rawIndex(),
        expires_at: "2027-01-01T00:00:00.000000001Z",
      }).expiresAt).toBe("2027-01-01T00:00:00.000000001Z");
    } finally {
      vi.useRealTimers();
    }
  });

  it.each([
    "queued", "running", "ready", "failed", "canceled", "expired",
  ] satisfies BackupArchiveMemberState[])("maps the closed %s member state", (state) => {
    expect(mapBackupArchiveStatus(rawStatusForState(state)).state).toBe(state);
  });

  it.each([
    "encrypted", "unsupported", "limit", "unsafe", "unavailable",
  ] satisfies BackupArchiveFailureProduct[])("maps the closed %s failure product", (failureProduct) => {
    const supportsOriginal = failureProduct === "encrypted" || failureProduct === "unsupported" || failureProduct === "limit";
    expect(mapBackupArchiveStatus(rawStatus({
      failure_product: failureProduct,
      fallback: supportsOriginal ? { action: "download_original" } : {},
    })).failureProduct).toBe(failureProduct);
  });

  it("accepts an opaque parent-path digest that is not a retrievable member", () => {
    expect(mapBackupArchiveIndex({
      ...rawIndex(),
      entries: [{
        ...rawIndex().entries[0],
        parent_id: "6".repeat(32),
      }],
    }).entries[0]).toMatchObject({
      id: memberId,
      parentId: "6".repeat(32),
    });
  });

  it("rejects a blank non-null archive parent ID", () => {
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      entries: [{ ...rawIndex().entries[0], parent_id: "" }],
    })).toThrow("invalid backup archive response");
  });

  it("requires a closed entry warning and rejects unknown warning values", () => {
    const entry = rawIndex().entries[0];
    const { warning: _warning, ...withoutWarning } = entry;

    expect(() => mapBackupArchiveIndex({ ...rawIndex(), entries: [withoutWarning] })).toThrow("invalid backup archive response");
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      entries: [{ ...entry, warning: "raw_worker_diagnostic" }],
    })).toThrow("invalid backup archive response");
  });

  it("accepts every server-supported archive index entry and rejects one more", () => {
    expect(mapBackupArchiveIndex(rawIndexWithEntries(100_000)).entries).toHaveLength(100_000);
    expect(() => mapBackupArchiveIndex(rawIndexWithEntries(100_001))).toThrow("invalid backup archive response");
  });

  it("rejects a cycle between present opaque parent entries", () => {
    const firstId = "8".repeat(32);
    const secondId = "9".repeat(32);
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      entries: [
        { ...rawIndex().entries[0], id: firstId, parent_id: secondId, display_name: "first" },
        { ...rawIndex().entries[0], id: secondId, parent_id: firstId, display_name: "second" },
      ],
    })).toThrow("invalid backup archive response");
  });

  it("rejects a raw archive path masquerading as a display name", () => {
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      entries: [{
        ...rawIndex().entries[0],
        display_name: "folder/secret.txt",
      }],
    })).toThrow("invalid backup archive response");
  });

  it("rejects duplicate display names within one opaque parent", () => {
    expect(() => mapBackupArchiveIndex({
      ...rawIndex(),
      entries: [
        rawIndex().entries[0],
        { ...rawIndex().entries[0], id: "7".repeat(32) },
      ],
    })).toThrow("invalid backup archive response");
  });

  it.each([
    ["unknown failure", { failure_product: "ratio_bomb_raw_reason" }],
    ["nested chain", { member_chain: [memberId, "5".repeat(32)] }],
    ["query-bearing status", { fallback: { action: "download_original", reason: "?secret" } }],
  ])("rejects unsafe archive products as a whole: %s", (_name, mutation) => {
    expect(() => mapBackupArchiveStatus(rawStatus(mutation))).toThrow();
  });

  it("uses exact composite routes and opaque member identity", async () => {
    const calls: Array<{ path: string; options: unknown }> = [];
    const requester = async (path: string, options: unknown) => {
      calls.push({ path, options });
      if (path.endsWith("archive-members")) return rawIndex();
      return rawCreate();
    };
    const api = createBackupArchiveApi(requester);
    await api.listIndex("token", { recoveryPointId, entryId });
    await api.create("token", { recoveryPointId, entryId }, "5".repeat(64), memberId, "archive-member-key-0001");
    expect(calls[0].path).toBe(`/recovery-points/${recoveryPointId}/entries/${entryId}/archive-members`);
    expect(calls[1]).toMatchObject({
      path: `/recovery-points/${recoveryPointId}/entries/${entryId}/archive-member-jobs`,
      options: {
        method: "POST",
        idempotencyKey: "archive-member-key-0001",
        body: { schema_version: 1, index_revision: "5".repeat(64), member_chain: [memberId] },
      },
    });
  });

  it("sends the frozen index revision on status and cancel", async () => {
    const requester = vi.fn().mockResolvedValue(rawStatus());
    const api = createBackupArchiveApi(requester);
    const ref = { recoveryPointId, entryId };

    await api.status("token", ref, indexRevision, requestId);
    await api.cancel("token", ref, indexRevision, requestId);

    expect(requester).toHaveBeenNthCalledWith(1,
      `/recovery-points/${recoveryPointId}/entries/${entryId}/archive-member-jobs/${requestId}?index_revision=${indexRevision}`,
      expect.objectContaining({ method: "GET", token: "token" }),
    );
    expect(requester).toHaveBeenNthCalledWith(2,
      `/recovery-points/${recoveryPointId}/entries/${entryId}/archive-member-jobs/${requestId}/cancel`,
      expect.objectContaining({
        method: "POST",
        token: "token",
        body: { schema_version: 1, index_revision: indexRevision },
      }),
    );
  });

  it("rejects a create response bound to a different outer asset", async () => {
    const requester = vi.fn().mockResolvedValue(rawCreate({
      asset_ref: { recovery_point_id: "6".repeat(32), entry_id: entryId },
    }));
    const api = createBackupArchiveApi(requester);

    await expect(api.create(
      "token",
      { recoveryPointId, entryId },
      indexRevision,
      memberId,
      "archive-member-key-0001",
    )).rejects.toThrow("invalid backup archive response");
  });

  it("rejects a status response bound to a different index revision", async () => {
    const requester = vi.fn().mockResolvedValue(rawStatus({ index_revision: "6".repeat(64) }));
    const api = createBackupArchiveApi(requester);

    await expect(api.status(
      "token",
      { recoveryPointId, entryId },
      indexRevision,
      requestId,
    )).rejects.toThrow("invalid backup archive response");
    expect(requester).toHaveBeenCalledOnce();
  });

  it("rejects a cancel response bound to a different outer asset", async () => {
    const requester = vi.fn().mockResolvedValue(rawStatus({
      asset_ref: { recovery_point_id: "6".repeat(32), entry_id: entryId },
    }));
    const api = createBackupArchiveApi(requester);

    await expect(api.cancel(
      "token",
      { recoveryPointId, entryId },
      indexRevision,
      requestId,
    )).rejects.toThrow("invalid backup archive response");
    expect(requester).toHaveBeenCalledOnce();
  });

  it("maps an exact member ticket with the backend range-none policy", async () => {
    const ticket = await createBackupArchiveApi(vi.fn().mockResolvedValue(rawTicket()))
      .issueTicket("token", { recoveryPointId, entryId }, requestId, "fresh-asset-download-proof");

    expect(ticket).toMatchObject({
      contentUrl: `/api/v1/asset-content/${requestId}`,
      range: "none",
    });
  });

  it("rejects an expired member ticket", async () => {
    const now = Date.now();
    const api = createBackupArchiveApi(vi.fn().mockResolvedValue({
      ...rawTicket(),
      expires_at: new Date(now - 1_000).toISOString(),
      idle_expires_at: new Date(now - 2_000).toISOString(),
    }));

    await expect(api.issueTicket(
      "token",
      { recoveryPointId, entryId },
      requestId,
      "fresh-asset-download-proof",
    )).rejects.toThrow("invalid backup archive response");
  });

  it("rejects an impossible timestamp in a member ticket", async () => {
    const api = createBackupArchiveApi(vi.fn().mockResolvedValue({
      ...rawTicket(),
      expires_at: "2999-02-30T00:00:00Z",
    }));

    await expect(api.issueTicket(
      "token",
      { recoveryPointId, entryId },
      requestId,
      "fresh-asset-download-proof",
    )).rejects.toThrow("invalid backup archive response");
  });

  it("canonicalizes member-ticket expiry fields without losing nanoseconds across offsets", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date("2026-12-31T15:59:59.999Z"));
      const ticket = await createBackupArchiveApi(vi.fn().mockResolvedValue({
        ...rawTicket(),
        expires_at: "2027-01-01T08:00:00.000123456+08:00",
        idle_expires_at: "2027-01-01T00:00:00.000123455Z",
      })).issueTicket(
        "token",
        { recoveryPointId, entryId },
        requestId,
        "fresh-asset-download-proof",
      );

      expect(ticket).toMatchObject({
        expiresAt: "2027-01-01T00:00:00.000123456Z",
        idleExpiresAt: "2027-01-01T00:00:00.000123455Z",
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("rejects a member ticket whose idle expiry is later only at nanosecond precision", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date("2026-12-31T15:59:59.999Z"));
      const api = createBackupArchiveApi(vi.fn().mockResolvedValue({
        ...rawTicket(),
        expires_at: "2027-01-01T00:00:00.100Z",
        idle_expires_at: "2027-01-01T00:00:00.100000001Z",
      }));

      await expect(api.issueTicket(
        "token",
        { recoveryPointId, entryId },
        requestId,
        "fresh-asset-download-proof",
      )).rejects.toThrow("invalid backup archive response");
    } finally {
      vi.useRealTimers();
    }
  });

  it("rejects a non-string member ETag even when coercion is syntactically valid", async () => {
    const api = createBackupArchiveApi(vi.fn().mockResolvedValue({
      ...rawTicket(),
      etag: { toString: () => '"member-etag"' },
    }));

    await expect(api.issueTicket(
      "token",
      { recoveryPointId, entryId },
      requestId,
      "fresh-asset-download-proof",
    )).rejects.toThrow("invalid backup archive response");
  });

  it.each([
    `https://example.invalid/api/v1/asset-content/${requestId}`,
    `/api/v1/asset-content/${requestId}?ticket=secret`,
  ])("rejects a non-canonical member ticket URL: %s", async (contentUrl) => {
    const api = createBackupArchiveApi(vi.fn().mockResolvedValue(rawTicket(contentUrl)));
    await expect(api.issueTicket(
      "token",
      { recoveryPointId, entryId },
      requestId,
      "fresh-asset-download-proof",
    )).rejects.toThrow("invalid backup archive response");
  });

  it("rejects a malformed member download proof before issuing a request", async () => {
    const requester = vi.fn();
    const api = createBackupArchiveApi(requester);

    await expect(api.issueTicket(
      "token",
      { recoveryPointId, entryId },
      requestId,
      "fresh\nasset-download-proof",
    )).rejects.toThrow("invalid backup archive request");
    expect(requester).not.toHaveBeenCalled();
  });
});
