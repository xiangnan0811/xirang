import { describe, expect, it } from "vitest";

import {
  backupAssetsPathHref,
  backupAssetsRecoveryPointHref,
  backupAssetsRestoreHref,
  backupAssetsSearchHref,
  backupAssetsTaskContextHref,
  defaultBackupAssetsRouteState,
  parseBackupAssetsRoute,
  serializeBackupAssetsRoute,
  updateBackupAssetsRoute,
  type BackupAssetsRouteState,
} from "./backup-assets-route-state";

const repositoryId = "a".repeat(32);
const recoveryPointId = "b".repeat(32);
const parentEntryId = "c".repeat(64);
const entryId = "d".repeat(64);
const savedSearchId = "e".repeat(32);

function expectValid(pathname: string, search = "") {
  const result = parseBackupAssetsRoute(pathname, search);
  expect(result.status).toBe("valid");
  if (result.status !== "valid") {
    throw new Error(`expected valid route: ${result.safePath}`);
  }
  return result.state;
}

describe("backup assets route state", () => {
  it("parses canonical page defaults and omits them when serializing", () => {
    const state = expectValid("/app/backups/data");

    expect(state).toEqual(defaultBackupAssetsRouteState("data"));
    expect(serializeBackupAssetsRoute(state)).toBe("/app/backups/data");
  });

  it("accepts only an opaque export job handle and round-trips it", () => {
    const exportJobId = "f".repeat(32);
    const state = expectValid("/app/backups/data", `?exportJobId=${exportJobId}`);
    expect(state.exportJobId).toBe(exportJobId);
    expect(serializeBackupAssetsRoute(state)).toBe(`/app/backups/data?exportJobId=${exportJobId}`);
  });

  it.each(["?exportJobId=not-opaque", "?exportJobId=ABC"].map((value) => [value]))(
    "rejects an unsafe export job handle %s",
    (search) => {
      expect(parseBackupAssetsRoute("/app/backups/data", search)).toEqual({
        status: "invalid",
        safePath: "/app/backups/data",
      });
    }
  );

  it("marks an incompatible export handle cleared by a repository change for replacement", () => {
    const source = expectValid("/app/backups/data", `?repositoryId=${repositoryId}&exportJobId=${"f".repeat(32)}`);
    const result = updateBackupAssetsRoute(source, { repositoryId: "0".repeat(32) });
    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state.exportJobId).toBeUndefined();
    expect(result).toMatchObject({ replace: true });
  });

  it("marks an incompatible export handle cleared by a view change for replacement", () => {
    const source = expectValid("/app/backups/data", `?repositoryId=${repositoryId}&exportJobId=${"f".repeat(32)}`);
    const result = updateBackupAssetsRoute(source, { view: "search" });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state.exportJobId).toBeUndefined();
    expect(result).toMatchObject({ replace: true });
  });

  it("accepts only closed fields and emits deterministic query ordering", () => {
    const state = expectValid(
      "/app/backups/data",
      `?direction=desc&type=unknown&repositoryId=${repositoryId}` +
        `&entryId=${entryId}&type=file&recoveryPointId=${recoveryPointId}` +
        `&parentEntryId=${parentEntryId}&favorite=true&tag=${repositoryId}` +
        "&sort=modified_at&layout=grid&inspectorTab=security"
    );

    expect(state.types).toEqual(["file", "unknown"]);
    expect(serializeBackupAssetsRoute(state)).toBe(
      `/app/backups/data?repositoryId=${repositoryId}` +
        `&recoveryPointId=${recoveryPointId}&parentEntryId=${parentEntryId}` +
        `&entryId=${entryId}&type=file&type=unknown&tag=${repositoryId}` +
        "&favorite=true&sort=modified_at&direction=desc&layout=grid" +
        "&inspectorTab=security"
    );
  });

  it.each([
    "?future=value",
    `?repositoryId=${repositoryId}&repositoryId=${repositoryId}`,
    "?type=file&type=file",
    "?taskId=01",
    "?taskId=0",
    "?favorite=false",
    `?repositoryId=${repositoryId.toUpperCase()}`,
    `?entryId=${entryId.slice(1)}`,
    "?sort=size&direction=asc",
    "?sort=modified_at&direction=asc",
    "?view=browse&sort=relevance&direction=desc",
  ])("rejects malformed, duplicate, unknown, or unsupported query %s", (search) => {
    expect(parseBackupAssetsRoute("/app/backups/data", search)).toEqual({
      status: "invalid",
      safePath: "/app/backups/data",
    });
  });

  it.each([
    `?entryId=${entryId}`,
    `?parentEntryId=${parentEntryId}`,
    `?scope=all_retained&recoveryPointId=${recoveryPointId}`,
    `?view=search&savedSearchId=${savedSearchId}&scope=all_retained`,
    `?view=search&savedSearchId=${savedSearchId}&type=file`,
    `?view=search&savedSearchId=${savedSearchId}&nodeId=7`,
    `?view=search&savedSearchId=${savedSearchId}&repositoryId=${repositoryId}`,
    `?view=repositories&taskId=7`,
    `?view=repositories&recoveryPointId=${recoveryPointId}`,
  ])("rejects invalid coupled data state %s", (search) => {
    expect(parseBackupAssetsRoute("/app/backups/data", search)).toEqual({
      status: "invalid",
      safePath: "/app/backups/data",
    });
  });

  it("round-trips a valid temporary search route", () => {
    const state: BackupAssetsRouteState = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search",
      repositoryId,
      taskId: 19,
      scope: "all_retained",
      types: ["directory", "file"],
      tagId: savedSearchId,
      favoriteOnly: true,
      sort: "name",
      direction: "asc",
      layout: "grid",
      inspectorTab: "metadata",
    };

    const href = serializeBackupAssetsRoute(state);
    expect(parseBackupAssetsRoute(...splitHref(href))).toEqual({
      status: "valid",
      state,
    });
  });

  it("preserves unrelated state while clearing only repository dependencies", () => {
    const source = expectValid(
      "/app/backups/data",
      `?repositoryId=${repositoryId}&recoveryPointId=${recoveryPointId}` +
        `&parentEntryId=${parentEntryId}&entryId=${entryId}&type=file` +
        "&favorite=true&sort=name&direction=desc&layout=grid"
    );

    const result = updateBackupAssetsRoute(source, {
      repositoryId: "f".repeat(32),
    });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      repositoryId: "f".repeat(32),
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
      types: ["file"],
      favoriteOnly: true,
      sort: "name",
      direction: "desc",
      layout: "grid",
    });
  });

  it("cleans incompatible fields when switching views", () => {
    const source = expectValid(
      "/app/backups/data",
      `?repositoryId=${repositoryId}&recoveryPointId=${recoveryPointId}` +
        `&parentEntryId=${parentEntryId}&entryId=${entryId}&type=file&favorite=true`
    );

    const result = updateBackupAssetsRoute(source, { view: "repositories" });

    expect(result).toEqual({
      status: "valid",
      state: {
        ...defaultBackupAssetsRouteState("data"),
        view: "repositories",
        repositoryId,
      },
      href: `/app/backups/data?view=repositories&repositoryId=${repositoryId}`,
    });
  });

  it("applies search view and all-retained scope atomically while clearing exact-point dependencies", () => {
    const source = expectValid(
      "/app/backups/data",
      `?repositoryId=${repositoryId}&recoveryPointId=${recoveryPointId}` +
        `&parentEntryId=${parentEntryId}&entryId=${entryId}&layout=grid`
    );

    const result = updateBackupAssetsRoute(source, { view: "search", scope: "all_retained" });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      view: "search",
      scope: "all_retained",
      repositoryId,
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
      sort: "relevance",
      direction: "desc",
      layout: "grid",
    });
    expect(result.href).toBe(
      `/app/backups/data?view=search&repositoryId=${repositoryId}&scope=all_retained&layout=grid`
    );
  });

  it("keeps an entry supplied atomically with its new recovery point", () => {
    const source = expectValid("/app/backups/data", `?repositoryId=${repositoryId}`);

    const result = updateBackupAssetsRoute(source, { recoveryPointId, entryId });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({ recoveryPointId, entryId });
    expect(result.href).toBe(
      `/app/backups/data?repositoryId=${repositoryId}&recoveryPointId=${recoveryPointId}&entryId=${entryId}`
    );
  });

  it("opens an exact retained search hit with one atomic route patch", () => {
    const source = expectValid(
      "/app/backups/data",
      `?view=search&repositoryId=${repositoryId}&scope=all_retained`
    );

    const result = updateBackupAssetsRoute(source, {
      view: "browse",
      recoveryPointId,
      entryId,
    });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      view: "browse",
      scope: "current",
      repositoryId,
      recoveryPointId,
      entryId,
    });
    expect(result.href).toBe(
      `/app/backups/data?repositoryId=${repositoryId}&recoveryPointId=${recoveryPointId}&entryId=${entryId}`
    );
  });

  it("preserves explicit browse-owned fields on an atomic search-to-browse patch", () => {
    const tagId = "1".repeat(32);
    const source = expectValid(
      "/app/backups/data",
      `?view=search&repositoryId=${repositoryId}&scope=all_retained&type=file&favorite=true`
    );

    const result = updateBackupAssetsRoute(source, {
      view: "browse",
      scope: "current",
      recoveryPointId,
      parentEntryId,
      entryId,
      types: ["file"],
      tagId,
      favoriteOnly: true,
      sort: "size",
      direction: "desc",
      layout: "grid",
    });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      view: "browse",
      scope: "current",
      repositoryId,
      recoveryPointId,
      parentEntryId,
      entryId,
      types: ["file"],
      tagId,
      favoriteOnly: true,
      sort: "size",
      direction: "desc",
      layout: "grid",
    });
    expect(result.href).toBe(
      `/app/backups/data?repositoryId=${repositoryId}&recoveryPointId=${recoveryPointId}` +
        `&parentEntryId=${parentEntryId}&entryId=${entryId}&type=file&tag=${tagId}` +
        "&favorite=true&sort=size&direction=desc&layout=grid"
    );
  });

  it("applies browse defaults when search-to-browse omits filter and sort fields", () => {
    const source = expectValid(
      "/app/backups/data",
      `?view=search&repositoryId=${repositoryId}&scope=all_retained&type=file&tag=${savedSearchId}&favorite=true`
    );

    const result = updateBackupAssetsRoute(source, {
      view: "browse",
      recoveryPointId,
      entryId,
    });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      view: "browse",
      scope: "current",
      repositoryId,
      recoveryPointId,
      entryId,
      types: [],
      favoriteOnly: false,
      sort: "name",
      direction: "asc",
    });
    expect(result.state.tagId).toBeUndefined();
    expect(result.href).toBe(
      `/app/backups/data?repositoryId=${repositoryId}&recoveryPointId=${recoveryPointId}&entryId=${entryId}`
    );
  });

  it("clears unverified source hierarchy while opening an all-retained search hit", () => {
    const backupSetId = "1".repeat(32);
    const source = expectValid(
      "/app/backups/data",
      `?view=search&nodeId=7&backupSetId=${backupSetId}&repositoryId=${repositoryId}&taskId=9&scope=all_retained`
    );

    const result = updateBackupAssetsRoute(source, {
      view: "browse",
      scope: "current",
      nodeId: undefined,
      backupSetId: undefined,
      repositoryId: undefined,
      taskId: undefined,
      recoveryPointId,
      entryId,
    });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      view: "browse",
      scope: "current",
      recoveryPointId,
      entryId,
    });
    expect(result.state.nodeId).toBeUndefined();
    expect(result.state.backupSetId).toBeUndefined();
    expect(result.state.repositoryId).toBeUndefined();
    expect(result.state.taskId).toBeUndefined();
    expect(result.href).toBe(
      `/app/backups/data?recoveryPointId=${recoveryPointId}&entryId=${entryId}`
    );
  });

  it("activates a saved search as an exclusive identity without source or filter state", () => {
    const backupSetId = "1".repeat(32);
    const source = expectValid(
      "/app/backups/data",
      `?nodeId=7&backupSetId=${backupSetId}&repositoryId=${repositoryId}&taskId=9&recoveryPointId=${recoveryPointId}&entryId=${entryId}`,
    );

    const result = updateBackupAssetsRoute(source, { savedSearchId });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      view: "search",
      savedSearchId,
      scope: "current",
      sort: "relevance",
      direction: "desc",
    });
    expect(result.state.nodeId).toBeUndefined();
    expect(result.state.backupSetId).toBeUndefined();
    expect(result.state.repositoryId).toBeUndefined();
    expect(result.state.taskId).toBeUndefined();
    expect(result.state.recoveryPointId).toBeUndefined();
    expect(result.state.parentEntryId).toBeUndefined();
    expect(result.state.entryId).toBeUndefined();
    expect(result.href).toBe(`/app/backups/data?view=search&savedSearchId=${savedSearchId}`);
  });

  it("exits saved search to browse when a source node is selected", () => {
    const source = expectValid("/app/backups/data", `?view=search&savedSearchId=${savedSearchId}`);

    const result = updateBackupAssetsRoute(source, { nodeId: 7 });

    expect(result.status).toBe("valid");
    if (result.status !== "valid") return;
    expect(result.state).toMatchObject({
      view: "browse",
      nodeId: 7,
      scope: "current",
      sort: "name",
      direction: "asc",
    });
    expect(result.state.savedSearchId).toBeUndefined();
  });

  it.each([
    "query=secret%20name",
    "path=%2Fprivate%2Fbackup",
    "name=payroll.csv",
    "cursor=ffffffffffffffffffffffffffffffff",
    "selection=asset-1",
    "ticket=%2Fapi%2Fv1%2Fasset-content%2Fsecret",
    "proof=step-up-proof",
    "reason=operator%20reason",
  ])("safe-resets forbidden browser-history payload %s", (query) => {
    const result = parseBackupAssetsRoute("/app/backups/data", `?${query}`);
    expect(result).toEqual({ status: "invalid", safePath: "/app/backups/data" });
    expect(JSON.stringify(result)).not.toContain(query.split("=")[1]);
  });

  it("keeps overview and recovery query products closed", () => {
    expect(parseBackupAssetsRoute("/app/backups/overview", "?layout=grid")).toEqual({
      status: "invalid",
      safePath: "/app/backups/overview",
    });
    expect(
      expectValid(
        "/app/backups/recovery",
        `?taskId=7&recoveryPointId=${recoveryPointId}&inspectorTab=evidence`
      )
    ).toMatchObject({ page: "recovery", taskId: 7, recoveryPointId, inspectorTab: "evidence" });
    expect(parseBackupAssetsRoute("/app/backups/recovery", "?view=search")).toEqual({
      status: "invalid",
      safePath: "/app/backups/recovery",
    });
  });

  it("carries only opaque recovery plan and job handles on the recovery route", () => {
    const planId = "d".repeat(32);
    const jobId = "e".repeat(32);
    const state = expectValid("/app/backups/recovery", `?planId=${planId}&jobId=${jobId}`);

    expect(state).toMatchObject({ page: "recovery", planId, jobId });
    expect(serializeBackupAssetsRoute(state)).toBe(`/app/backups/recovery?planId=${planId}&jobId=${jobId}`);
    expect(parseBackupAssetsRoute("/app/backups/recovery", `?jobId=${jobId}`)).toEqual({
      status: "invalid",
      safePath: "/app/backups/recovery",
    });
    expect(parseBackupAssetsRoute("/app/backups/recovery", `?planId=${planId}&grantSecret=secret`)).toEqual({
      status: "invalid",
      safePath: "/app/backups/recovery",
    });
  });

  it("builds a task-context compatibility link without legacy snapshot or path state", () => {
    const href = backupAssetsTaskContextHref(101);

    expect(href).toBe("/app/backups/data?taskId=101");
    expect(href).not.toMatch(/snapshot|path|query|entryId|recoveryPointId/);
    expect(backupAssetsTaskContextHref(0)).toBe("/app/backups/data");
  });

  it("maps a snapshot-equivalent recovery point onto the data workspace", () => {
    const href = backupAssetsRecoveryPointHref(101, recoveryPointId);

    expect(href).toBe(`/app/backups/data?taskId=101&recoveryPointId=${recoveryPointId}`);
    expect(parseBackupAssetsRoute(...splitHref(href))).toEqual({
      status: "valid",
      state: {
        ...defaultBackupAssetsRouteState("data"),
        taskId: 101,
        recoveryPointId,
      },
    });
    expect(href).not.toMatch(/snapshot|path|query/);
    expect(backupAssetsRecoveryPointHref(101, "snap-legacy")).toBe("/app/backups/data?taskId=101");
    expect(backupAssetsRecoveryPointHref(0, recoveryPointId)).toBe(
      `/app/backups/data?recoveryPointId=${recoveryPointId}`
    );
  });

  it("maps path and search onto opaque workspace routes instead of snapshot query state", () => {
    const searchHref = backupAssetsSearchHref(19);
    const pathHref = backupAssetsPathHref(19, recoveryPointId, parentEntryId, entryId);

    expect(searchHref).toBe("/app/backups/data?view=search&taskId=19");
    expect(parseBackupAssetsRoute(...splitHref(searchHref))).toEqual({
      status: "valid",
      state: {
        ...defaultBackupAssetsRouteState("data"),
        view: "search",
        taskId: 19,
        sort: "relevance",
        direction: "desc",
      },
    });
    expect(pathHref).toBe(
      `/app/backups/data?taskId=19&recoveryPointId=${recoveryPointId}` +
        `&parentEntryId=${parentEntryId}&entryId=${entryId}`
    );
    expect(parseBackupAssetsRoute(...splitHref(pathHref))).toEqual({
      status: "valid",
      state: {
        ...defaultBackupAssetsRouteState("data"),
        taskId: 19,
        recoveryPointId,
        parentEntryId,
        entryId,
      },
    });
    expect(searchHref).not.toMatch(/snapshot|path|query=/);
    expect(pathHref).not.toMatch(/snapshot|path=|query=/);
    expect(backupAssetsPathHref(19, recoveryPointId, "/private/backup")).toBe(
      `/app/backups/data?taskId=19&recoveryPointId=${recoveryPointId}`
    );
    expect(backupAssetsSearchHref(0)).toBe("/app/backups/data?view=search");
  });

  it("maps restore entry into /app/backups/recovery without legacy snapshot restore state", () => {
    const href = backupAssetsRestoreHref(501, recoveryPointId);

    expect(href).toBe(`/app/backups/recovery?taskId=501&recoveryPointId=${recoveryPointId}`);
    expect(parseBackupAssetsRoute(...splitHref(href))).toEqual({
      status: "valid",
      state: {
        ...defaultBackupAssetsRouteState("recovery"),
        taskId: 501,
        recoveryPointId,
        inspectorTab: "evidence",
      },
    });
    expect(href).not.toMatch(/snapshot|path|query|grant|ticket/);
    expect(backupAssetsRestoreHref(501)).toBe("/app/backups/recovery?taskId=501");
    expect(backupAssetsRestoreHref(0, "snap-legacy")).toBe("/app/backups/recovery");
  });
});

function splitHref(href: string): [string, string] {
  const index = href.indexOf("?");
  return index === -1 ? [href, ""] : [href.slice(0, index), href.slice(index)];
}
