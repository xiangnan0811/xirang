import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import {
  createBackupAssetOverlaysApi,
  mapFavorite,
  mapRecentAccess,
  mapSavedSearch,
  mapTag,
} from "./backup-asset-overlays-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return { ...actual, request: vi.fn() };
});

const requestMock = vi.mocked(request);
const pointId = "1".repeat(32);
const entryId = "a".repeat(64);
const overlayId = "2".repeat(32);
const now = () => new Date(Date.now()).toISOString();
const future = () => new Date(Date.now() + 60 * 60 * 1000).toISOString();

function rawQuery() {
  return {
    schema_version: 1,
    root: { op: "term", field: "name", text: "report" },
    scope: { mode: "current" },
    sort: "relevance",
    limit: 25,
  };
}

describe("backup asset overlays API boundary", () => {
  beforeEach(() => requestMock.mockReset());

  it("maps valid saved search, favorite, tag, and recent products", () => {
    expect(mapSavedSearch({ id: overlayId, query: rawQuery(), version: 1, state: "active", created_at: now(), updated_at: now() }).status).toBe("available");
    expect(mapSavedSearch({ id: overlayId, query: rawQuery(), version: 2, state: "blocked", state_reason: "ast_schema_unsupported", broken_at: null, created_at: now(), updated_at: now() }).status).toBe("available");
    expect(mapFavorite({ id: overlayId, ref: { recovery_point_id: pointId, entry_id: entryId }, label: "mine", state: "tombstone", tombstone_reason: "source_expired", version: 2, created_at: now(), updated_at: now() }).status).toBe("available");
    expect(mapTag({ id: overlayId, name: "finance", version: 1, created_at: now(), updated_at: now() }).status).toBe("available");
    expect(mapRecentAccess({ id: overlayId, ref: { recovery_point_id: pointId, entry_id: entryId }, access_count: 2, last_accessed_at: now(), expires_at: future(), version: 2 }).status).toBe("available");
  });

  it.each([
    ["unknown saved state", () => mapSavedSearch({ id: overlayId, query: rawQuery(), version: 1, state: "future", created_at: now(), updated_at: now() })],
    ["active favorite with tombstone reason", () => mapFavorite({ id: overlayId, ref: { recovery_point_id: pointId, entry_id: entryId }, state: "active", tombstone_reason: "source_expired", version: 1, created_at: now(), updated_at: now() })],
    ["invalid tag version", () => mapTag({ id: overlayId, name: "finance", version: 0, created_at: now(), updated_at: now() })],
    ["unsafe recent count", () => mapRecentAccess({ id: overlayId, ref: { recovery_point_id: pointId, entry_id: entryId }, access_count: Number.MAX_SAFE_INTEGER + 1, last_accessed_at: now(), expires_at: future(), version: 1 })],
    ["recent expiry not after last access", () => mapRecentAccess({ id: overlayId, ref: { recovery_point_id: pointId, entry_id: entryId }, access_count: 1, last_accessed_at: "2026-07-18T10:00:00Z", expires_at: "2026-07-18T09:00:00Z", version: 1 })],
  ])("blocks the whole overlay for %s", (_name, map) => {
    expect(map()).toEqual({ status: "blocked", reason: { code: "unknown_internal_state", params: {} } });
  });

  it("forwards idempotency through the central request options", async () => {
    requestMock.mockResolvedValueOnce({ id: overlayId, ref: { recovery_point_id: pointId, entry_id: entryId }, state: "active", version: 1, created_at: now(), updated_at: now() });
    const api = createBackupAssetOverlaysApi();
    await api.addFavorite("token", { recoveryPointId: pointId, entryId }, "mine", "favorite-key-0001");
    expect(requestMock).toHaveBeenCalledWith("/asset-favorites", {
      method: "POST",
      token: "token",
      idempotencyKey: "favorite-key-0001",
      signal: undefined,
      body: { ref: { recovery_point_id: pointId, entry_id: entryId }, label: "mine" },
    });
  });

  it("keeps saved AST in request bodies and opaque IDs in paths", async () => {
    requestMock.mockResolvedValueOnce({ id: overlayId, query: rawQuery(), version: 1, state: "active", created_at: now(), updated_at: now() });
    const api = createBackupAssetOverlaysApi();
    await api.createSavedSearch("token", {
      schemaVersion: 1,
      root: { op: "term", field: "name", text: "report" },
      scope: { mode: "current", repositoryIds: [], taskIds: [], recoveryPointIds: [] },
      sort: "relevance",
      limit: 25,
      cursor: null,
    }, "saved-search-key-01");
    const [path, options] = requestMock.mock.calls[0];
    expect(path).toBe("/asset-saved-searches");
    expect(JSON.stringify(options)).not.toContain("?query=");
    expect(options).toMatchObject({ idempotencyKey: "saved-search-key-01" });
  });
});

describe("backup asset overlays browser-state boundary", () => {
  it("does not persist query, path, selection, or result state", async () => {
    const source = await import("./backup-asset-overlays-api?raw");
    const text = String(source.default);
    for (const forbidden of ["localStorage", "sessionStorage", "history", "location", "router"]) {
      expect(text).not.toContain(forbidden);
    }
  });
});
