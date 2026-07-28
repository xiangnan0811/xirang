import type { CatalogEntryType } from "@/types/domain";

export type BackupsPageRoute = "overview" | "data" | "recovery";
export type BackupAssetsDataView = "browse" | "search" | "repositories";
export type BackupAssetsScope = "current" | "all_retained";
export type BackupAssetsSortField = "relevance" | "name" | "size" | "modified_at";
export type BackupAssetsSortDirection = "asc" | "desc";
export type BackupAssetsLayout = "list" | "grid";
export type BackupAssetsInspectorTab =
  | "preview"
  | "metadata"
  | "versions"
  | "security"
  | "evidence"
  | "diff";

export interface BackupAssetsRouteState {
  page: BackupsPageRoute;
  view: BackupAssetsDataView;
  repositoryId?: string;
  taskId?: number;
  recoveryPointId?: string;
  parentEntryId?: string;
  entryId?: string;
  savedSearchId?: string;
  exportJobId?: string;
  scope: BackupAssetsScope;
  types: CatalogEntryType[];
  tagId?: string;
  favoriteOnly: boolean;
  sort: BackupAssetsSortField;
  direction: BackupAssetsSortDirection;
  layout: BackupAssetsLayout;
  inspectorTab: BackupAssetsInspectorTab;
}

export type BackupAssetsRouteResult =
  | { status: "valid"; state: BackupAssetsRouteState }
  | { status: "invalid"; safePath: string };

export type BackupAssetsRouteUpdateResult =
  | { status: "valid"; state: BackupAssetsRouteState; href: string; replace?: true }
  | { status: "invalid"; safePath: string };

const DATA_PATH = "/app/backups/data";
const OVERVIEW_PATH = "/app/backups/overview";
const RECOVERY_PATH = "/app/backups/recovery";
const OPAQUE_ID_PATTERN = /^[0-9a-f]{32}$/;
const ENTRY_ID_PATTERN = /^[0-9a-f]{64}$/;
const POSITIVE_INTEGER_PATTERN = /^[1-9][0-9]*$/;
const DATA_QUERY_KEYS = new Set([
  "view",
  "repositoryId",
  "taskId",
  "recoveryPointId",
  "parentEntryId",
  "entryId",
  "savedSearchId",
  "exportJobId",
  "scope",
  "type",
  "tag",
  "favorite",
  "sort",
  "direction",
  "layout",
  "inspectorTab",
]);
const RECOVERY_QUERY_KEYS = new Set(["taskId", "recoveryPointId", "inspectorTab"]);
const ENTRY_TYPES: readonly CatalogEntryType[] = [
  "directory",
  "file",
  "hardlink",
  "special",
  "symlink",
  "unknown",
];
const INSPECTOR_TABS: readonly BackupAssetsInspectorTab[] = [
  "preview",
  "metadata",
  "versions",
  "security",
  "evidence",
  "diff",
];

export function defaultBackupAssetsRouteState(page: BackupsPageRoute): BackupAssetsRouteState {
  return {
    page,
    view: "browse",
    scope: "current",
    types: [],
    favoriteOnly: false,
    sort: "name",
    direction: "asc",
    layout: "list",
    inspectorTab: page === "recovery" ? "evidence" : "preview",
  };
}

export function parseBackupAssetsRoute(pathname: string, search: string): BackupAssetsRouteResult {
  const page = pageFromPath(pathname);
  if (page === null) {
    return invalid(OVERVIEW_PATH);
  }

  const params = new URLSearchParams(search);
  if (page === "overview") {
    return params.size === 0 ? { status: "valid", state: defaultBackupAssetsRouteState(page) } : invalid(OVERVIEW_PATH);
  }
  if (page === "recovery") {
    return parseRecoveryRoute(params);
  }
  return parseDataRoute(params);
}

export function serializeBackupAssetsRoute(state: BackupAssetsRouteState): string {
  const normalized = normalizeState(state);
  if (!isValidState(normalized)) {
    return pathForPage(state.page);
  }

  const params = new URLSearchParams();
  if (normalized.page === "overview") return OVERVIEW_PATH;
  if (normalized.page === "recovery") {
    appendNumber(params, "taskId", normalized.taskId);
    appendString(params, "recoveryPointId", normalized.recoveryPointId);
    if (normalized.inspectorTab !== "evidence") params.set("inspectorTab", normalized.inspectorTab);
    return withQuery(RECOVERY_PATH, params);
  }

  const defaults = defaultsForView(normalized.view);
  if (normalized.view !== "browse") params.set("view", normalized.view);
  appendString(params, "repositoryId", normalized.repositoryId);
  appendNumber(params, "taskId", normalized.taskId);
  appendString(params, "recoveryPointId", normalized.recoveryPointId);
  appendString(params, "parentEntryId", normalized.parentEntryId);
  appendString(params, "entryId", normalized.entryId);
  appendString(params, "savedSearchId", normalized.savedSearchId);
  appendString(params, "exportJobId", normalized.exportJobId);
  if (normalized.scope !== "current") params.set("scope", normalized.scope);
  for (const type of normalized.types) params.append("type", type);
  appendString(params, "tag", normalized.tagId);
  if (normalized.favoriteOnly) params.set("favorite", "true");
  if (normalized.sort !== defaults.sort || normalized.direction !== defaults.direction) {
    params.set("sort", normalized.sort);
    params.set("direction", normalized.direction);
  }
  if (normalized.layout !== "list") params.set("layout", normalized.layout);
  if (normalized.inspectorTab !== "preview") params.set("inspectorTab", normalized.inspectorTab);
  return withQuery(DATA_PATH, params);
}

export function backupAssetsTaskContextHref(taskId: number): string {
  return serializeBackupAssetsRoute({
    ...defaultBackupAssetsRouteState("data"),
    taskId: Number.isSafeInteger(taskId) && taskId > 0 ? taskId : undefined,
  });
}

export function updateBackupAssetsRoute(
  source: BackupAssetsRouteState,
  patch: Partial<BackupAssetsRouteState>
): BackupAssetsRouteUpdateResult {
  if (!isValidState(normalizeState(source))) return invalid(pathForPage(source.page));

  if (patch.page !== undefined && patch.page !== source.page) {
    const state = normalizeState({ ...defaultBackupAssetsRouteState(patch.page), ...patch });
    return updateResult(state, source.exportJobId !== undefined && state.exportJobId === undefined);
  }

  let state: BackupAssetsRouteState = { ...source, ...patch };
  const repositoryChanged = hasOwn(patch, "repositoryId") && patch.repositoryId !== source.repositoryId;
  const recoveryPointChanged =
    hasOwn(patch, "recoveryPointId") && patch.recoveryPointId !== source.recoveryPointId;
  const parentChanged = hasOwn(patch, "parentEntryId") && patch.parentEntryId !== source.parentEntryId;

  if (repositoryChanged) {
    state = {
      ...state,
      recoveryPointId: hasOwn(patch, "recoveryPointId") ? state.recoveryPointId : undefined,
      parentEntryId: hasOwn(patch, "parentEntryId") ? state.parentEntryId : undefined,
      entryId: hasOwn(patch, "entryId") ? state.entryId : undefined,
      exportJobId: hasOwn(patch, "exportJobId") ? state.exportJobId : undefined,
    };
  } else if (recoveryPointChanged) {
    state = {
      ...state,
      parentEntryId: hasOwn(patch, "parentEntryId") ? state.parentEntryId : undefined,
      entryId: hasOwn(patch, "entryId") ? state.entryId : undefined,
      exportJobId: hasOwn(patch, "exportJobId") ? state.exportJobId : undefined,
    };
  } else if (parentChanged) {
    state = { ...state, entryId: hasOwn(patch, "entryId") ? state.entryId : undefined };
  }

  if (patch.view !== undefined && patch.view !== source.view) {
    const viewDefaults = defaultsForView(patch.view);
    state = {
      ...state,
      savedSearchId: undefined,
      scope: patch.scope ?? "current",
      types: [],
      tagId: undefined,
      favoriteOnly: false,
      sort: viewDefaults.sort,
      direction: viewDefaults.direction,
      parentEntryId: hasOwn(patch, "parentEntryId") ? state.parentEntryId : undefined,
      entryId: hasOwn(patch, "entryId") ? state.entryId : undefined,
      exportJobId: hasOwn(patch, "exportJobId") ? state.exportJobId : undefined,
    };
    if (patch.view === "repositories") {
      state = {
        ...defaultBackupAssetsRouteState("data"),
        view: "repositories",
        repositoryId: state.repositoryId,
        layout: state.layout,
        exportJobId: undefined,
      };
    }
  }

  if (hasOwn(patch, "savedSearchId") && patch.savedSearchId !== undefined) {
    const viewDefaults = defaultsForView("search");
    state = {
      ...state,
      view: "search",
      scope: "current",
      types: [],
      tagId: undefined,
      favoriteOnly: false,
      sort: viewDefaults.sort,
      direction: viewDefaults.direction,
      exportJobId: undefined,
    };
  }

  if (state.scope === "all_retained") {
    state = {
      ...state,
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
      exportJobId: undefined,
    };
  }

  state = normalizeState(state);
  const clearedIncompatibleExportJob =
    source.exportJobId !== undefined &&
    state.exportJobId === undefined &&
    (
      repositoryChanged ||
      recoveryPointChanged ||
      (patch.view !== undefined && patch.view !== source.view) ||
      (hasOwn(patch, "savedSearchId") && patch.savedSearchId !== undefined) ||
      (source.scope !== "all_retained" && state.scope === "all_retained")
    );
  return updateResult(state, clearedIncompatibleExportJob);
}

function parseRecoveryRoute(params: URLSearchParams): BackupAssetsRouteResult {
  if (!hasOnlyKeys(params, RECOVERY_QUERY_KEYS) || hasDuplicateSingular(params)) return invalid(RECOVERY_PATH);
  const taskId = parseTaskId(params.get("taskId"));
  const recoveryPointId = parseOpaqueId(params.get("recoveryPointId"));
  const tab = params.get("inspectorTab") ?? "evidence";
  if (
    (params.has("taskId") && taskId === undefined) ||
    (params.has("recoveryPointId") && recoveryPointId === undefined) ||
    (tab !== "evidence" && tab !== "diff")
  ) {
    return invalid(RECOVERY_PATH);
  }
  return {
    status: "valid",
    state: {
      ...defaultBackupAssetsRouteState("recovery"),
      taskId,
      recoveryPointId,
      inspectorTab: tab,
    },
  };
}

function parseDataRoute(params: URLSearchParams): BackupAssetsRouteResult {
  if (!hasOnlyKeys(params, DATA_QUERY_KEYS) || hasDuplicateSingular(params)) return invalid(DATA_PATH);

  const view = parseOneOf(params.get("view") ?? "browse", ["browse", "search", "repositories"] as const);
  const repositoryId = parseOpaqueId(params.get("repositoryId"));
  const taskId = parseTaskId(params.get("taskId"));
  const recoveryPointId = parseOpaqueId(params.get("recoveryPointId"));
  const parentEntryId = parseEntryId(params.get("parentEntryId"));
  const entryId = parseEntryId(params.get("entryId"));
  const savedSearchId = parseOpaqueId(params.get("savedSearchId"));
  const exportJobId = parseOpaqueId(params.get("exportJobId"));
  const scope = parseOneOf(params.get("scope") ?? "current", ["current", "all_retained"] as const);
  const types = params.getAll("type");
  const tagId = parseOpaqueId(params.get("tag"));
  const favorite = params.get("favorite");
  const layout = parseOneOf(params.get("layout") ?? "list", ["list", "grid"] as const);
  const inspectorTab = parseOneOf(params.get("inspectorTab") ?? "preview", INSPECTOR_TABS);

  if (
    view === undefined ||
    scope === undefined ||
    layout === undefined ||
    inspectorTab === undefined ||
    (params.has("repositoryId") && repositoryId === undefined) ||
    (params.has("taskId") && taskId === undefined) ||
    (params.has("recoveryPointId") && recoveryPointId === undefined) ||
    (params.has("parentEntryId") && parentEntryId === undefined) ||
    (params.has("entryId") && entryId === undefined) ||
    (params.has("savedSearchId") && savedSearchId === undefined) ||
    (params.has("exportJobId") && exportJobId === undefined) ||
    (params.has("tag") && tagId === undefined) ||
    (favorite !== null && favorite !== "true") ||
    types.length > 6 ||
    new Set(types).size !== types.length ||
    !types.every(isEntryType)
  ) {
    return invalid(DATA_PATH);
  }

  const viewDefaults = defaultsForView(view);
  const sort = parseOneOf(params.get("sort") ?? viewDefaults.sort, ["relevance", "name", "size", "modified_at"] as const);
  const direction = parseOneOf(params.get("direction") ?? viewDefaults.direction, ["asc", "desc"] as const);
  if (sort === undefined || direction === undefined) return invalid(DATA_PATH);

  const state = normalizeState({
    page: "data",
    view,
    repositoryId,
    taskId,
    recoveryPointId,
    parentEntryId,
    entryId,
    savedSearchId,
    exportJobId,
    scope,
    types: types as CatalogEntryType[],
    tagId,
    favoriteOnly: favorite === "true",
    sort,
    direction,
    layout,
    inspectorTab,
  });

  return isValidState(state) ? { status: "valid", state } : invalid(DATA_PATH);
}

function normalizeState(state: BackupAssetsRouteState): BackupAssetsRouteState {
  return { ...state, types: [...state.types].sort() };
}

function isValidState(state: BackupAssetsRouteState): boolean {
  if (!isPage(state.page) || !isDataView(state.view) || !isScope(state.scope)) return false;
  if (!isLayout(state.layout) || !INSPECTOR_TABS.includes(state.inspectorTab)) return false;
  if (state.repositoryId !== undefined && !OPAQUE_ID_PATTERN.test(state.repositoryId)) return false;
  if (state.taskId !== undefined && (!Number.isSafeInteger(state.taskId) || state.taskId <= 0)) return false;
  if (state.recoveryPointId !== undefined && !OPAQUE_ID_PATTERN.test(state.recoveryPointId)) return false;
  if (state.parentEntryId !== undefined && !ENTRY_ID_PATTERN.test(state.parentEntryId)) return false;
  if (state.entryId !== undefined && !ENTRY_ID_PATTERN.test(state.entryId)) return false;
  if (state.savedSearchId !== undefined && !OPAQUE_ID_PATTERN.test(state.savedSearchId)) return false;
  if (state.exportJobId !== undefined && !OPAQUE_ID_PATTERN.test(state.exportJobId)) return false;
  if (state.tagId !== undefined && !OPAQUE_ID_PATTERN.test(state.tagId)) return false;
  if (state.types.length > 6 || new Set(state.types).size !== state.types.length || !state.types.every(isEntryType)) return false;

  if (state.page === "overview") return equalsDefaults(state, defaultBackupAssetsRouteState("overview"));
  if (state.page === "recovery") {
    return (
      state.view === "browse" &&
      state.repositoryId === undefined &&
      state.parentEntryId === undefined &&
      state.entryId === undefined &&
      state.savedSearchId === undefined &&
      state.exportJobId === undefined &&
      state.scope === "current" &&
      state.types.length === 0 &&
      state.tagId === undefined &&
      !state.favoriteOnly &&
      state.sort === "name" &&
      state.direction === "asc" &&
      state.layout === "list" &&
      (state.inspectorTab === "evidence" || state.inspectorTab === "diff")
    );
  }

  if ((state.parentEntryId !== undefined || state.entryId !== undefined) && state.recoveryPointId === undefined) return false;
  if (state.scope === "all_retained" && state.recoveryPointId !== undefined) return false;
  if (state.view === "repositories") {
    return (
      state.taskId === undefined &&
      state.recoveryPointId === undefined &&
      state.parentEntryId === undefined &&
      state.entryId === undefined &&
      state.savedSearchId === undefined &&
      state.exportJobId === undefined &&
      state.scope === "current" &&
      state.types.length === 0 &&
      state.tagId === undefined &&
      !state.favoriteOnly &&
      state.sort === "name" &&
      state.direction === "asc" &&
      state.inspectorTab === "preview"
    );
  }
  if (state.savedSearchId !== undefined) {
    if (
      state.view !== "search" ||
      state.scope !== "current" ||
      state.types.length > 0 ||
      state.tagId !== undefined ||
      state.favoriteOnly ||
      state.sort !== "relevance" ||
      state.direction !== "desc"
    ) {
      return false;
    }
  }
  return validSortPair(state.view, state.sort, state.direction);
}

function validSortPair(
  view: BackupAssetsDataView,
  sort: BackupAssetsSortField,
  direction: BackupAssetsSortDirection
): boolean {
  if (view === "browse") {
    return (
      (sort === "name" && (direction === "asc" || direction === "desc")) ||
      (sort === "size" && direction === "desc") ||
      (sort === "modified_at" && direction === "desc")
    );
  }
  if (view === "search") {
    return (
      (sort === "relevance" && direction === "desc") ||
      (sort === "name" && direction === "asc") ||
      (sort === "modified_at" && direction === "desc")
    );
  }
  return sort === "name" && direction === "asc";
}

function defaultsForView(view: BackupAssetsDataView): Pick<BackupAssetsRouteState, "sort" | "direction"> {
  return view === "search" ? { sort: "relevance", direction: "desc" } : { sort: "name", direction: "asc" };
}

function updateResult(state: BackupAssetsRouteState, replace = false): BackupAssetsRouteUpdateResult {
  if (!isValidState(state)) return invalid(pathForPage(state.page));
  const result = { status: "valid" as const, state, href: serializeBackupAssetsRoute(state) };
  return replace ? { ...result, replace: true } : result;
}

function invalid(safePath: string): { status: "invalid"; safePath: string } {
  return { status: "invalid", safePath };
}

function pageFromPath(pathname: string): BackupsPageRoute | null {
  if (pathname === OVERVIEW_PATH) return "overview";
  if (pathname === DATA_PATH) return "data";
  if (pathname === RECOVERY_PATH) return "recovery";
  return null;
}

function pathForPage(page: BackupsPageRoute): string {
  if (page === "data") return DATA_PATH;
  if (page === "recovery") return RECOVERY_PATH;
  return OVERVIEW_PATH;
}

function hasOnlyKeys(params: URLSearchParams, allowed: ReadonlySet<string>): boolean {
  return [...params.keys()].every((key) => allowed.has(key));
}

function hasDuplicateSingular(params: URLSearchParams): boolean {
  return [...new Set(params.keys())].some((key) => key !== "type" && params.getAll(key).length !== 1);
}

function parseOpaqueId(value: string | null): string | undefined {
  return value !== null && OPAQUE_ID_PATTERN.test(value) ? value : undefined;
}

function parseEntryId(value: string | null): string | undefined {
  return value !== null && ENTRY_ID_PATTERN.test(value) ? value : undefined;
}

function parseTaskId(value: string | null): number | undefined {
  if (value === null || !POSITIVE_INTEGER_PATTERN.test(value)) return undefined;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : undefined;
}

function parseOneOf<const T extends string>(value: string, options: readonly T[]): T | undefined {
  return options.find((option) => option === value);
}

function isEntryType(value: string): value is CatalogEntryType {
  return ENTRY_TYPES.some((entryType) => entryType === value);
}

function isPage(value: string): value is BackupsPageRoute {
  return value === "overview" || value === "data" || value === "recovery";
}

function isDataView(value: string): value is BackupAssetsDataView {
  return value === "browse" || value === "search" || value === "repositories";
}

function isScope(value: string): value is BackupAssetsScope {
  return value === "current" || value === "all_retained";
}

function isLayout(value: string): value is BackupAssetsLayout {
  return value === "list" || value === "grid";
}

function equalsDefaults(state: BackupAssetsRouteState, defaults: BackupAssetsRouteState): boolean {
  return JSON.stringify(state) === JSON.stringify(defaults);
}

function hasOwn<T extends object>(value: T, key: PropertyKey): boolean {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function appendString(params: URLSearchParams, key: string, value: string | undefined): void {
  if (value !== undefined) params.set(key, value);
}

function appendNumber(params: URLSearchParams, key: string, value: number | undefined): void {
  if (value !== undefined) params.set(key, String(value));
}

function withQuery(pathname: string, params: URLSearchParams): string {
  const query = params.toString();
  return query === "" ? pathname : `${pathname}?${query}`;
}
