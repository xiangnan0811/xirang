import type { BackupAssetsUIError } from "@/lib/api/backup-assets-error";
import type {
  AssetRef,
  AssetSearchHitField,
  AssetSearchSnippet,
  BackupAsset,
  BackupAssetDirectoryContext,
} from "@/types/domain";

import type { BackupAssetsRouteState } from "./backup-assets-route-state";

export interface BackupAssetResultRow {
  ref: AssetRef;
  asset: BackupAsset;
  source: "browse" | "search";
  hitFields: AssetSearchHitField[];
  snippet: AssetSearchSnippet | null;
  retainedVersionCount?: number;
}

export type BackupAssetsResultCoverage = "complete" | "partial" | "building" | "failed" | "unavailable";

export interface BackupAssetsResultState {
  status: "idle" | "loading" | "ready" | "failed";
  requestKey: string | null;
  generation: number;
  rows: BackupAssetResultRow[];
  nextCursor: string | null;
  coverage: BackupAssetsResultCoverage;
  authoritativeEmpty: boolean;
  directory: BackupAssetDirectoryContext | null;
  error?: BackupAssetsUIError;
}

export type BackupAssetsOverlayState =
  | { status: "idle" }
  | { status: "pending" | "reconciling"; attemptKey: string; operation: string }
  | { status: "failed"; attemptKey: string; operation: string };

export type BackupAssetsTicketState =
  | { status: "idle" }
  | { status: "issuing"; bindingKey: string }
  | { status: "ready"; bindingKey: string; contentUrl: string; expiresAt: string }
  | { status: "failed"; bindingKey: string };

export interface BackupAssetsState {
  route: BackupAssetsRouteState;
  contextGeneration: number;
  selectionGeneration: number;
  result: BackupAssetsResultState;
  selection: Map<string, AssetRef>;
  searchDraft: string;
  submittedSearchQuery: string | null;
  overlay: BackupAssetsOverlayState;
  ticket: BackupAssetsTicketState;
  tombstone: "repository" | "recovery_point" | "parent" | "entry" | null;
}

export type BackupAssetsAction =
  | { type: "route_changed"; route: BackupAssetsRouteState }
  | { type: "results_loading"; requestKey: string; replace?: boolean }
  | {
      type: "results_replaced";
      requestKey: string;
      rows: BackupAssetResultRow[];
      nextCursor: string | null;
      coverage: BackupAssetsResultCoverage;
      authoritativeEmpty: boolean;
      directory: BackupAssetDirectoryContext | null;
    }
  | {
      type: "results_appended";
      requestKey: string;
      rows: BackupAssetResultRow[];
      nextCursor: string | null;
      directory: BackupAssetDirectoryContext | null;
    }
  | { type: "results_failed"; requestKey: string; error?: BackupAssetsUIError }
  | { type: "cursor_stale"; requestKey: string }
  | { type: "toggle_selection"; ref: AssetRef }
  | { type: "clear_selection" }
  | { type: "search_draft_changed"; text: string }
  | { type: "search_submitted"; query: string }
  | { type: "search_cleared" }
  | { type: "overlay_started"; attemptKey: string; operation: string }
  | { type: "overlay_reconciling" }
  | { type: "overlay_failed" }
  | { type: "overlay_completed" }
  | { type: "ticket_issuing"; bindingKey: string }
  | { type: "ticket_ready"; bindingKey: string; contentUrl: string; expiresAt: string }
  | { type: "ticket_failed"; bindingKey: string }
  | { type: "ticket_detached" }
  | { type: "tombstone"; target: "repository" | "recovery_point" | "parent" | "entry" };

export function createInitialBackupAssetsState(route: BackupAssetsRouteState): BackupAssetsState {
  return {
    route,
    contextGeneration: 1,
    selectionGeneration: 1,
    result: emptyResultState(),
    selection: new Map(),
    searchDraft: "",
    submittedSearchQuery: null,
    overlay: { status: "idle" },
    ticket: { status: "idle" },
    tombstone: null,
  };
}

export function backupAssetsReducer(state: BackupAssetsState, action: BackupAssetsAction): BackupAssetsState {
  switch (action.type) {
    case "route_changed":
      return reduceRouteChange(state, action.route);
    case "results_loading": {
      const replace = action.replace ?? action.requestKey !== state.result.requestKey;
      if (!replace) {
        return {
          ...state,
          result: {
            ...state.result,
            status: "loading",
            requestKey: action.requestKey,
            authoritativeEmpty: false,
          },
        };
      }
      return {
        ...state,
        result: {
          status: "loading",
          requestKey: action.requestKey,
          generation: state.result.generation + 1,
          rows: [],
          nextCursor: null,
          coverage: "unavailable",
          authoritativeEmpty: false,
          directory: null,
        },
        selection: new Map(),
      };
    }
    case "results_replaced":
      if (state.result.requestKey !== action.requestKey) return state;
      return {
        ...state,
        result: {
          status: "ready",
          requestKey: action.requestKey,
          generation: state.result.generation + 1,
          rows: deduplicateRows(action.rows),
          nextCursor: action.nextCursor,
          coverage: action.coverage,
          authoritativeEmpty: action.authoritativeEmpty,
          directory: action.directory,
        },
        selection: new Map(),
        tombstone: null,
      };
    case "results_appended": {
      if (state.result.requestKey !== action.requestKey) return state;
      if (!sameDirectoryContext(state.result.directory, action.directory)) {
        return {
          ...state,
          result: {
            ...emptyResultState(),
            status: "failed",
            requestKey: action.requestKey,
            generation: state.result.generation + 1,
          },
          selection: new Map(),
          selectionGeneration: state.selectionGeneration + 1,
        };
      }
      return {
        ...state,
        result: {
          ...state.result,
          status: "ready",
          rows: deduplicateRows([...state.result.rows, ...action.rows]),
          nextCursor: action.nextCursor,
          error: undefined,
        },
      };
    }
    case "results_failed":
      return state.result.requestKey === action.requestKey
        ? { ...state, result: { ...state.result, status: "failed", error: action.error } }
        : state;
    case "cursor_stale":
      return state.result.requestKey === action.requestKey
        ? {
            ...state,
            result: {
              status: "loading",
              requestKey: action.requestKey,
              generation: state.result.generation + 1,
              rows: [],
              nextCursor: null,
              coverage: state.result.coverage,
              authoritativeEmpty: false,
              directory: null,
            },
            selection: new Map(),
            selectionGeneration: state.selectionGeneration + 1,
          }
        : state;
    case "toggle_selection": {
      const selection = new Map(state.selection);
      const key = assetRefKey(action.ref);
      if (selection.has(key)) selection.delete(key);
      else selection.set(key, action.ref);
      return { ...state, selection };
    }
    case "clear_selection":
      return state.selection.size === 0 ? state : { ...state, selection: new Map() };
    case "search_draft_changed":
      return { ...state, searchDraft: action.text };
    case "search_submitted":
      return state.submittedSearchQuery === action.query
        ? state
        : { ...state, submittedSearchQuery: action.query };
    case "search_cleared":
      return {
        ...state,
        searchDraft: "",
        submittedSearchQuery: null,
        contextGeneration: state.contextGeneration + 1,
        selectionGeneration: state.selectionGeneration + 1,
        result: { ...emptyResultState(), generation: state.result.generation + 1 },
        selection: new Map(),
        ticket: { status: "idle" },
        tombstone: null,
      };
    case "overlay_started":
      return {
        ...state,
        overlay: {
          status: "pending",
          attemptKey: action.attemptKey,
          operation: action.operation,
        },
      };
    case "overlay_reconciling":
      return state.overlay.status === "pending"
        ? { ...state, overlay: { ...state.overlay, status: "reconciling" } }
        : state;
    case "overlay_failed":
      return state.overlay.status === "pending" || state.overlay.status === "reconciling"
        ? { ...state, overlay: { ...state.overlay, status: "failed" } }
        : state;
    case "overlay_completed":
      return { ...state, overlay: { status: "idle" } };
    case "ticket_issuing":
      return { ...state, ticket: { status: "issuing", bindingKey: action.bindingKey } };
    case "ticket_ready":
      return state.ticket.status === "issuing" && state.ticket.bindingKey !== action.bindingKey
        ? state
        : {
            ...state,
            ticket: {
              status: "ready",
              bindingKey: action.bindingKey,
              contentUrl: action.contentUrl,
              expiresAt: action.expiresAt,
            },
          };
    case "ticket_failed":
      return { ...state, ticket: { status: "failed", bindingKey: action.bindingKey } };
    case "ticket_detached":
      return { ...state, ticket: { status: "idle" } };
    case "tombstone":
      return reduceTombstone(state, action.target);
  }
}

export function assetRefKey(ref: AssetRef): string {
  return `${ref.recoveryPointId}:${ref.entryId}`;
}

function reduceRouteChange(state: BackupAssetsState, route: BackupAssetsRouteState): BackupAssetsState {
  const repositoryChanged = state.route.repositoryId !== route.repositoryId;
  const nodeChanged = state.route.nodeId !== route.nodeId;
  const backupSetChanged = state.route.backupSetId !== route.backupSetId;
  const taskChanged = state.route.taskId !== route.taskId;
  const recoveryPointChanged = state.route.recoveryPointId !== route.recoveryPointId;
  const parentChanged = state.route.parentEntryId !== route.parentEntryId;
  const entryChanged = state.route.entryId !== route.entryId;
  const viewChanged = state.route.view !== route.view;
  const resultContextChanged =
    nodeChanged ||
    backupSetChanged ||
    repositoryChanged ||
    taskChanged ||
    recoveryPointChanged ||
    parentChanged ||
    viewChanged ||
    state.route.savedSearchId !== route.savedSearchId ||
    state.route.scope !== route.scope ||
    state.route.tagId !== route.tagId ||
    state.route.favoriteOnly !== route.favoriteOnly ||
    state.route.sort !== route.sort ||
    state.route.direction !== route.direction ||
    state.route.types.join(",") !== route.types.join(",");

  if (resultContextChanged) {
    const activatingSaved =
      route.savedSearchId !== undefined && state.route.savedSearchId !== route.savedSearchId;
    return {
      ...state,
      route,
      contextGeneration: state.contextGeneration + 1,
      selectionGeneration: state.selectionGeneration + 1,
      result: { ...emptyResultState(), generation: state.result.generation + 1 },
      selection: new Map(),
      searchDraft: activatingSaved ? "" : state.searchDraft,
      submittedSearchQuery: activatingSaved ? null : route.view === "search" ? state.submittedSearchQuery : null,
      ticket: { status: "idle" },
      tombstone: null,
    };
  }
  if (entryChanged) {
    return {
      ...state,
      route,
      selectionGeneration: state.selectionGeneration + 1,
      ticket: { status: "idle" },
      tombstone: null,
    };
  }
  return { ...state, route };
}

function reduceTombstone(
  state: BackupAssetsState,
  target: "repository" | "recovery_point" | "parent" | "entry"
): BackupAssetsState {
  const route = { ...state.route };
  if (target === "repository") {
    route.repositoryId = undefined;
    route.recoveryPointId = undefined;
    route.parentEntryId = undefined;
    route.entryId = undefined;
  } else if (target === "recovery_point") {
    route.recoveryPointId = undefined;
    route.parentEntryId = undefined;
    route.entryId = undefined;
  } else if (target === "parent") {
    route.parentEntryId = undefined;
    route.entryId = undefined;
  } else {
    route.entryId = undefined;
  }
  return {
    ...state,
    route,
    contextGeneration: state.contextGeneration + 1,
    selectionGeneration: state.selectionGeneration + 1,
    result: target === "entry" ? state.result : { ...emptyResultState(), generation: state.result.generation + 1 },
    selection: new Map(),
    ticket: { status: "idle" },
    overlay: { status: "idle" },
    tombstone: target,
  };
}

function emptyResultState(): BackupAssetsResultState {
  return {
    status: "idle",
    requestKey: null,
    generation: 0,
    rows: [],
    nextCursor: null,
    coverage: "unavailable",
    authoritativeEmpty: false,
    directory: null,
  };
}

function sameDirectoryContext(
  left: BackupAssetDirectoryContext | null,
  right: BackupAssetDirectoryContext | null,
): boolean {
  if (left === null || right === null) return left === right;
  if (left.current === null || right.current === null) {
    if (left.current !== right.current) return false;
  } else if (!sameAssetRef(left.current.ref, right.current.ref) || left.current.name !== right.current.name) return false;
  if (left.parent === null || right.parent === null) {
    if (left.parent !== right.parent) return false;
  } else if (!sameAssetRef(left.parent, right.parent)) return false;
  return left.breadcrumb.length === right.breadcrumb.length && left.breadcrumb.every((item, index) => {
    const other = right.breadcrumb[index];
    return other !== undefined && item.name === other.name && sameAssetRef(item.ref, other.ref);
  });
}

function sameAssetRef(left: AssetRef, right: AssetRef): boolean {
  return left.recoveryPointId === right.recoveryPointId && left.entryId === right.entryId;
}

function deduplicateRows(rows: BackupAssetResultRow[]): BackupAssetResultRow[] {
  const seen = new Set<string>();
  return rows.filter((row) => {
    const key = assetRefKey(row.ref);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export interface BackupAssetsRestorationAnchor {
  contextKey: string;
  ref: AssetRef;
  index: number;
  offset: number;
}

export interface BackupAssetsRestorationRegistry {
  record(anchor: BackupAssetsRestorationAnchor): void;
  read(contextKey: string): BackupAssetsRestorationAnchor | null;
  clear(contextKey?: string): void;
}

export function createBackupAssetsRestorationRegistry(): BackupAssetsRestorationRegistry {
  const anchors = new Map<string, BackupAssetsRestorationAnchor>();
  return {
    record(anchor) {
      if (!validAnchor(anchor)) return;
      anchors.set(anchor.contextKey, { ...anchor, ref: { ...anchor.ref } });
    },
    read(contextKey) {
      const anchor = anchors.get(contextKey);
      return anchor === undefined ? null : { ...anchor, ref: { ...anchor.ref } };
    },
    clear(contextKey) {
      if (contextKey === undefined) anchors.clear();
      else anchors.delete(contextKey);
    },
  };
}

function validAnchor(anchor: BackupAssetsRestorationAnchor): boolean {
  return (
    /^[0-9a-f:|-]{32,256}$/.test(anchor.contextKey) &&
    /^[0-9a-f]{32}$/.test(anchor.ref.recoveryPointId) &&
    /^[0-9a-f]{64}$/.test(anchor.ref.entryId) &&
    Number.isSafeInteger(anchor.index) &&
    anchor.index >= 0 &&
    Number.isFinite(anchor.offset) &&
    anchor.offset >= 0
  );
}
