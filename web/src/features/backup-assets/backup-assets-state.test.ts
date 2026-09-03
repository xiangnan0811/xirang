import { describe, expect, it, vi } from "vitest";

import {
  assetRefKey,
  backupAssetsReducer,
  createBackupAssetsRestorationRegistry,
  createInitialBackupAssetsState,
  type BackupAssetResultRow,
} from "./backup-assets-state";
import { defaultBackupAssetsRouteState } from "./backup-assets-route-state";
import type { AssetRef, BackupAsset, BackupAssetDirectoryContext } from "@/types/domain";

const repositoryId = "a".repeat(32);
const nextRepositoryId = "b".repeat(32);
const recoveryPointId = "c".repeat(32);
const nextRecoveryPointId = "d".repeat(32);
const parentEntryId = "e".repeat(64);

function ref(entryCharacter: string, point = recoveryPointId): AssetRef {
  return { recoveryPointId: point, entryId: entryCharacter.repeat(64) };
}

function row(entryCharacter: string, point = recoveryPointId): BackupAssetResultRow {
  const assetRef = ref(entryCharacter, point);
  const asset: BackupAsset = {
    ref: assetRef,
    parentRef: null,
    name: `synthetic-${entryCharacter}`,
    entryType: "file",
    size: 12,
    modifiedAt: "2026-07-19T00:00:00Z",
    mode: "0640",
    owner: "operator",
    mimeType: "text/plain",
    fingerprintStrength: "strong",
    breadcrumb: [],
  };
  return { ref: assetRef, asset, source: "browse", hitFields: [], snippet: null };
}

function route(overrides = {}) {
  return {
    ...defaultBackupAssetsRouteState("data"),
    repositoryId,
    recoveryPointId,
    ...overrides,
  };
}

function directory(currentCharacter = "e", parentCharacter?: string): BackupAssetDirectoryContext {
  const current = { ref: ref(currentCharacter), name: `directory-${currentCharacter}` };
  const parent = parentCharacter ? ref(parentCharacter) : null;
  return {
    current,
    parent,
    breadcrumb: parent
      ? [{ ref: parent, name: `directory-${parentCharacter}` }, current]
      : [current],
  };
}

describe("backupAssetsReducer", () => {
  it("clears every dependent product when repository changes", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:one" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "browse:one",
      rows: [row("1")],
      nextCursor: "f".repeat(32),
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    });
    state = backupAssetsReducer(state, { type: "toggle_selection", ref: ref("1") });
    state = backupAssetsReducer(state, {
      type: "ticket_ready",
      bindingKey: assetRefKey(ref("1")),
      contentUrl: `/api/v1/asset-content/${"9".repeat(32)}`,
      expiresAt: "2026-07-20T00:00:00Z",
    });

    const next = backupAssetsReducer(state, {
      type: "route_changed",
      route: route({ repositoryId: nextRepositoryId, recoveryPointId: undefined }),
    });

    expect(next.route.repositoryId).toBe(nextRepositoryId);
    expect(next.result.rows).toEqual([]);
    expect(next.selection.size).toBe(0);
    expect(next.ticket.status).toBe("idle");
    expect(next.contextGeneration).toBeGreaterThan(state.contextGeneration);
    expect(next.selectionGeneration).toBeGreaterThan(state.selectionGeneration);
  });

  it("clears parent, entry, result, and ticket when the exact recovery point changes", () => {
    const source = createInitialBackupAssetsState(
      route({ parentEntryId, entryId: "1".repeat(64) })
    );
    const next = backupAssetsReducer(source, {
      type: "route_changed",
      route: route({ recoveryPointId: nextRecoveryPointId, parentEntryId: undefined, entryId: undefined }),
    });

    expect(next.route.recoveryPointId).toBe(nextRecoveryPointId);
    expect(next.route.parentEntryId).toBeUndefined();
    expect(next.route.entryId).toBeUndefined();
    expect(next.result.rows).toEqual([]);
    expect(next.ticket.status).toBe("idle");
  });

  it("deduplicates cursor pages and keeps selection within one result generation", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:one" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "browse:one",
      rows: [row("1"), row("2")],
      nextCursor: "f".repeat(32),
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    });
    state = backupAssetsReducer(state, { type: "toggle_selection", ref: ref("2") });
    state = backupAssetsReducer(state, {
      type: "results_appended",
      requestKey: "browse:one",
      rows: [row("2"), row("3")],
      nextCursor: null,
      directory: null,
    });

    expect(state.result.rows.map((item) => item.asset.name)).toEqual([
      "synthetic-1",
      "synthetic-2",
      "synthetic-3",
    ]);
    expect(state.selection.has(assetRefKey(ref("2")))).toBe(true);
  });

  it("ignores an append for an obsolete result key", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:new" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "browse:new",
      rows: [row("1")],
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    });

    expect(
      backupAssetsReducer(state, {
        type: "results_appended",
        requestKey: "browse:old",
        rows: [row("2")],
        nextCursor: null,
        directory: null,
      })
    ).toBe(state);
  });


  it("clears stale rows immediately on a first-page replacement load", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:one" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "browse:one",
      rows: [row("1")],
      nextCursor: "f".repeat(32),
      coverage: "partial",
      authoritativeEmpty: false,
      directory: null,
    });
    state = backupAssetsReducer(state, { type: "toggle_selection", ref: ref("1") });

    const next = backupAssetsReducer(state, {
      type: "results_loading",
      requestKey: "browse:one",
      replace: true,
    });

    expect(next.result.rows).toEqual([]);
    expect(next.result.nextCursor).toBeNull();
    expect(next.result.status).toBe("loading");
    expect(next.selection.size).toBe(0);
    expect(next.result.error).toBeUndefined();
  });

  it("preserves current rows while appending the same request", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:one" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "browse:one",
      rows: [row("1")],
      nextCursor: "f".repeat(32),
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    });

    const next = backupAssetsReducer(state, {
      type: "results_loading",
      requestKey: "browse:one",
      replace: false,
    });

    expect(next.result.rows).toEqual([row("1")]);
    expect(next.result.nextCursor).toBe("f".repeat(32));
    expect(next.result.status).toBe("loading");
  });

  it("stores a mapped result error without keeping stale rows from another source", () => {
    let state = createInitialBackupAssetsState(route({ nodeId: 3, taskId: 7, backupSetId: "1".repeat(32) }));
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "search:docker" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "search:docker",
      rows: [row("1")],
      nextCursor: null,
      coverage: "partial",
      authoritativeEmpty: false,
      directory: null,
    });

    const failed = backupAssetsReducer(state, {
      type: "results_failed",
      requestKey: "search:docker",
      error: {
        code: "temporarily_unavailable",
        translationKey: "backupAssets.errors.temporarilyUnavailable",
        retryable: true,
        action: "retry",
      },
    });
    expect(failed.result.status).toBe("failed");
    expect(failed.result.error?.translationKey).toBe("backupAssets.errors.temporarilyUnavailable");
    expect(failed.result.coverage).toBe("partial");

    const switched = backupAssetsReducer(failed, {
      type: "route_changed",
      route: route({ nodeId: 4, taskId: 8, backupSetId: "2".repeat(32) }),
    });
    expect(switched.result.rows).toEqual([]);
    expect(switched.result.status).toBe("idle");
    expect(switched.contextGeneration).toBeGreaterThan(failed.contextGeneration);

    expect(
      backupAssetsReducer(switched, {
        type: "results_replaced",
        requestKey: "search:docker",
        rows: [row("9")],
        nextCursor: null,
        coverage: "complete",
        authoritativeEmpty: false,
        directory: null,
      })
    ).toBe(switched);
  });

  it("snapshots a submitted query independently of the draft and clears it on explicit clear", () => {
    let state = createInitialBackupAssetsState(route({ view: "search", sort: "relevance", direction: "desc" }));
    state = backupAssetsReducer(state, { type: "search_draft_changed", text: "docker" });
    state = backupAssetsReducer(state, { type: "search_submitted", query: "docker" });
    state = backupAssetsReducer(state, { type: "search_draft_changed", text: "docker-compose" });
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "search:docker" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "search:docker",
      rows: [row("1")],
      nextCursor: "f".repeat(32),
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    });
    state = backupAssetsReducer(state, {
      type: "ticket_ready",
      bindingKey: assetRefKey(ref("1")),
      contentUrl: `/api/v1/asset-content/${"9".repeat(32)}`,
      expiresAt: "2026-07-20T00:00:00Z",
    });

    expect(state.submittedSearchQuery).toBe("docker");
    expect(state.searchDraft).toBe("docker-compose");

    const cleared = backupAssetsReducer(state, { type: "search_cleared" });
    expect(cleared.submittedSearchQuery).toBeNull();
    expect(cleared.searchDraft).toBe("");
    expect(cleared.result.rows).toEqual([]);
    expect(cleared.result.nextCursor).toBeNull();
    expect(cleared.selection.size).toBe(0);
    expect(cleared.ticket.status).toBe("idle");
  });
  it("ignores a replacement for an obsolete result key", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:new" });

    expect(
      backupAssetsReducer(state, {
        type: "results_replaced",
        requestKey: "browse:old",
        rows: [row("2")],
        nextCursor: null,
        coverage: "complete",
        authoritativeEmpty: false,
        directory: null,
      })
    ).toBe(state);
  });

  it("resets stale cursor pages while retaining the in-memory search draft", () => {
    let state = createInitialBackupAssetsState(route({ view: "search", sort: "relevance", direction: "desc" }));
    state = backupAssetsReducer(state, { type: "search_draft_changed", text: "synthetic query" });
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "search:one" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "search:one",
      rows: [row("1")],
      nextCursor: "f".repeat(32),
      coverage: "partial",
      authoritativeEmpty: false,
      directory: null,
    });

    state = backupAssetsReducer(state, { type: "cursor_stale", requestKey: "search:one" });

    expect(state.result.rows).toEqual([]);
    expect(state.result.status).toBe("loading");
    expect(state.result.nextCursor).toBeNull();
    expect(state.searchDraft).toBe("synthetic query");
  });

  it("keeps one explicit directory context across cursor pages and fails closed on contradiction", () => {
    let state = createInitialBackupAssetsState(route({ parentEntryId }));
    const current = directory();
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:directory" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "browse:directory",
      rows: [row("1")],
      nextCursor: "next",
      coverage: "complete",
      authoritativeEmpty: false,
      directory: current,
    });
    state = backupAssetsReducer(state, {
      type: "results_appended",
      requestKey: "browse:directory",
      rows: [row("2")],
      nextCursor: null,
      directory: current,
    });
    expect(state.result.directory).toEqual(current);
    expect(state.result.rows).toHaveLength(2);

    state = backupAssetsReducer(state, {
      type: "results_appended",
      requestKey: "browse:directory",
      rows: [row("3")],
      nextCursor: null,
      directory: directory("e", "d"),
    });
    expect(state.result.status).toBe("failed");
    expect(state.result.rows).toEqual([]);
    expect(state.result.directory).toBeNull();
  });

  it("fails closed when a root cursor page contradicts the canonical empty breadcrumb", () => {
    const root: BackupAssetDirectoryContext = { current: null, parent: null, breadcrumb: [] };
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "browse:root" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "browse:root",
      rows: [row("1")],
      nextCursor: "next",
      coverage: "complete",
      authoritativeEmpty: false,
      directory: root,
    });
    state = backupAssetsReducer(state, {
      type: "results_appended",
      requestKey: "browse:root",
      rows: [row("2")],
      nextCursor: null,
      directory: { ...root, breadcrumb: [{ ref: ref("e"), name: "contradiction" }] },
    });

    expect(state.result.status).toBe("failed");
    expect(state.result.rows).toEqual([]);
    expect(state.result.directory).toBeNull();
  });

  it("turns an exact tombstone into a blocked state and drops sensitive transient state", () => {
    let state = createInitialBackupAssetsState(route({ entryId: "1".repeat(64) }));
    state = backupAssetsReducer(state, { type: "toggle_selection", ref: ref("1") });
    state = backupAssetsReducer(state, {
      type: "ticket_issuing",
      bindingKey: assetRefKey(ref("1")),
    });

    state = backupAssetsReducer(state, { type: "tombstone", target: "entry" });

    expect(state.tombstone).toBe("entry");
    expect(state.selection.size).toBe(0);
    expect(state.ticket.status).toBe("idle");
    expect(state.route.entryId).toBeUndefined();
  });

  it("keeps one in-memory overlay attempt through reconciliation and clears it on completion", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, {
      type: "overlay_started",
      attemptKey: "attempt-0001",
      operation: "favorite_add",
    });
    expect(state.overlay).toEqual({
      status: "pending",
      attemptKey: "attempt-0001",
      operation: "favorite_add",
    });

    state = backupAssetsReducer(state, { type: "overlay_reconciling" });
    expect(state.overlay.status).toBe("reconciling");
    expect(state.overlay).toMatchObject({ attemptKey: "attempt-0001" });

    state = backupAssetsReducer(state, { type: "overlay_completed" });
    expect(state.overlay).toEqual({ status: "idle" });
  });

  it("detaches a ready ticket without retaining its opaque URL", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, {
      type: "ticket_ready",
      bindingKey: assetRefKey(ref("1")),
      contentUrl: `/api/v1/asset-content/${"9".repeat(32)}`,
      expiresAt: "2026-07-20T00:00:00Z",
    });

    state = backupAssetsReducer(state, { type: "ticket_detached" });
    expect(state.ticket).toEqual({ status: "idle" });
    expect(JSON.stringify(state)).not.toContain("asset-content");
  });

  it("retains current rows and cursor when the same request fails after a page", () => {
    let state = createInitialBackupAssetsState(route());
    state = backupAssetsReducer(state, { type: "results_loading", requestKey: "search:docker" });
    state = backupAssetsReducer(state, {
      type: "results_replaced",
      requestKey: "search:docker",
      rows: [row("1")],
      nextCursor: "f".repeat(32),
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    });

    const failed = backupAssetsReducer(state, {
      type: "results_failed",
      requestKey: "search:docker",
      error: {
        code: "temporarily_unavailable",
        translationKey: "backupAssets.errors.temporarilyUnavailable",
        retryable: true,
        action: "retry",
      },
    });

    expect(failed.result.status).toBe("failed");
    expect(failed.result.rows).toEqual([row("1")]);
    expect(failed.result.nextCursor).toBe("f".repeat(32));
  });

  it("clears temporary query and selection when a saved search becomes active", () => {
    let state = createInitialBackupAssetsState(route({
      view: "search",
      sort: "relevance",
      direction: "desc",
    }));
    state.searchDraft = "term";
    state.submittedSearchQuery = "term";
    state.selection = new Map([[assetRefKey(ref("1")), ref("1")]]);

    state = backupAssetsReducer(state, {
      type: "route_changed",
      route: route({
        view: "search",
        savedSearchId: "e".repeat(32),
        sort: "relevance",
        direction: "desc",
        repositoryId: undefined,
        recoveryPointId: undefined,
      }),
    });

    expect(state.searchDraft).toBe("");
    expect(state.submittedSearchQuery).toBeNull();
    expect(state.selection.size).toBe(0);
    expect(state.result.rows).toEqual([]);
  });
});

describe("backup asset restoration registry", () => {
  it("keeps opaque focus/scroll anchors in memory without touching browser storage", () => {
    const localSet = vi.spyOn(Storage.prototype, "setItem");
    const historyReplace = vi.spyOn(window.history, "replaceState");
    const registry = createBackupAssetsRestorationRegistry();

    registry.record({
      contextKey: `${repositoryId}:${recoveryPointId}`,
      ref: ref("1"),
      index: 17,
      offset: 420,
    });

    expect(registry.read(`${repositoryId}:${recoveryPointId}`)).toEqual({
      contextKey: `${repositoryId}:${recoveryPointId}`,
      ref: ref("1"),
      index: 17,
      offset: 420,
    });
    expect(localSet).not.toHaveBeenCalled();
    expect(historyReplace).not.toHaveBeenCalled();
    localSet.mockRestore();
    historyReplace.mockRestore();
  });
});
