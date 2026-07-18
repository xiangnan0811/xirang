import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import { createBackupAssetSearchApi, mapBackupAssetSearch } from "./backup-asset-search-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return { ...actual, request: vi.fn() };
});

const requestMock = vi.mocked(request);
const pointId = "1".repeat(32);
const catalogGenerationId = "2".repeat(32);
const searchGenerationId = "3".repeat(32);
const entryId = "a".repeat(64);

function rawAsset() {
  return {
    recovery_point_id: pointId,
    entry_id: entryId,
    parent_entry_id: null,
    name: "report.txt",
    entry_type: "file",
    size: 12,
    modified_at: "2026-07-18T00:00:00Z",
    mode: "0640",
    owner: "backup",
    mime_type: "text/plain",
    fingerprint_strength: "strong",
    breadcrumb: [],
  };
}

function rawSearch() {
  return {
    query_generation: "f".repeat(64),
    indexes: [{
      recovery_point_id: pointId,
      catalog_generation_id: catalogGenerationId,
      search_generation_id: searchGenerationId,
      projection_revision: 1,
      coverage: "complete",
      staleness: "fresh",
    }],
    items: [{
      ref: { recovery_point_id: pointId, entry_id: entryId },
      asset: rawAsset(),
      hit_fields: ["name"],
      score: 100,
      snippet: null,
    }],
    next_cursor: "opaque-cursor",
    total: 1,
    total_relation: "exact",
    authoritative_empty: false,
    coverage: { status: "complete" },
    suggestions: [],
    capabilities: { metadata: true, content: false },
    permissions: { list: true, secret_reveal: false },
  };
}

describe("backup asset search API boundary", () => {
  beforeEach(() => requestMock.mockReset());

  it("maps one coupled search response to camelCase", () => {
    const mapped = mapBackupAssetSearch(rawSearch());
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") throw new Error("expected available search");
    expect(mapped.value).toMatchObject({
      queryGeneration: "f".repeat(64),
      indexes: [{ recoveryPointId: pointId, catalogGenerationId, searchGenerationId, projectionRevision: 1 }],
      items: [{
        ref: { recoveryPointId: pointId, entryId },
        asset: { ref: { recoveryPointId: pointId, entryId }, name: "report.txt" },
        hitFields: ["name"],
        score: 100,
      }],
      total: 1,
      totalRelation: "exact",
      authoritativeEmpty: false,
      coverage: { status: "complete" },
      permissions: { list: true, secretReveal: false },
    });
  });

  it("preserves covered metadata hits when aggregate coverage is partial", () => {
    const complete = rawSearch();
    const raw = {
      ...complete,
      indexes: [{ ...complete.indexes[0], coverage: "partial" }],
      coverage: { status: "partial" },
      total: null,
      total_relation: "unavailable",
    };
    const mapped = mapBackupAssetSearch(raw);
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") throw new Error("expected available partial search");
    expect(mapped.value.items).toHaveLength(1);
    expect(mapped.value.items[0].hitFields).toEqual(["name"]);
    expect(mapped.value.authoritativeEmpty).toBe(false);
  });

  it("accepts non-secret content hits without requiring a secret-reveal proof", () => {
    const complete = rawSearch();
    const raw = {
      ...complete,
      items: [{
        ...complete.items[0],
        hit_fields: ["content"],
        snippet: { field: "content", text: "verified excerpt" },
      }],
      capabilities: { metadata: true, content: true },
      permissions: { list: true, secret_reveal: false },
    };
    const mapped = mapBackupAssetSearch(raw);
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") throw new Error("expected available content search");
    expect(mapped.value.items[0]).toMatchObject({
      hitFields: ["content"],
      snippet: { field: "content", text: "verified excerpt" },
    });
  });

  it.each([
    ["unknown coverage", () => ({ ...rawSearch(), coverage: { status: "future" } })],
    ["contradictory empty", () => ({ ...rawSearch(), total: 0, authoritative_empty: true })],
    ["complete aggregate with partial index", () => ({
      ...rawSearch(),
      indexes: [{ ...rawSearch().indexes[0], coverage: "partial" }],
    })],
    ["missing generation", () => ({ ...rawSearch(), indexes: [{ ...rawSearch().indexes[0], search_generation_id: "" }] })],
    ["invalid ref", () => ({ ...rawSearch(), items: [{ ...rawSearch().items[0], ref: { recovery_point_id: "bad", entry_id: entryId } }] })],
    ["content hit without capability", () => ({ ...rawSearch(), items: [{ ...rawSearch().items[0], hit_fields: ["content"] }] })],
    ["content suggestion without capability", () => ({ ...rawSearch(), suggestions: [{ field: "content", value: "hidden" }] })],
  ])("blocks the whole projection for %s", (_name, mutate) => {
    const mapped = mapBackupAssetSearch(mutate());
    expect(mapped).toEqual({ status: "blocked", reason: { code: "unknown_internal_state", params: {} } });
    expect(JSON.stringify(mapped)).not.toContain("report.txt");
  });

  it("posts inline queries in the body and only forwards the exact reveal proof", async () => {
    requestMock.mockResolvedValueOnce(rawSearch());
    const api = createBackupAssetSearchApi();
    await api.search("token", {
      query: {
        schemaVersion: 1,
        root: { op: "term", field: "name", text: "report" },
        scope: { mode: "current", repositoryIds: [], taskIds: [], recoveryPointIds: [] },
        sort: "relevance",
        limit: 25,
        cursor: null,
      },
      secretRevealProof: "proof-secret",
    });
    expect(requestMock).toHaveBeenCalledWith("/asset-search", {
      method: "POST",
      token: "token",
      stepUpProof: "proof-secret",
      signal: undefined,
      body: {
        query: {
          schema_version: 1,
          root: { op: "term", field: "name", text: "report" },
          scope: { mode: "current" },
          sort: "relevance",
          limit: 25,
        },
      },
    });
    expect(JSON.stringify(requestMock.mock.calls)).not.toContain("?query=");
  });

  it("uses only an opaque saved-search ID in the request body", async () => {
    requestMock.mockResolvedValueOnce(rawSearch());
    const api = createBackupAssetSearchApi();
    await api.search("token", { savedSearchId: "4".repeat(32), limit: 10, cursor: "opaque-cursor" });
    expect(requestMock).toHaveBeenCalledWith("/asset-search", expect.objectContaining({
      body: { saved_search_id: "4".repeat(32), limit: 10, cursor: "opaque-cursor" },
    }));
  });
});

describe("backup asset search browser-state boundary", () => {
  it("does not persist or URL-encode query state", async () => {
    const source = await import("./backup-asset-search-api?raw");
    const text = String(source.default);
    for (const forbidden of ["localStorage", "sessionStorage", "history", "location", "router", "URLSearchParams"]) {
      expect(text).not.toContain(forbidden);
    }
  });
});
