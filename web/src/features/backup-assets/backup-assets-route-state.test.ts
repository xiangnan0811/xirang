import { describe, expect, it } from "vitest";

import {
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

  it("builds a task-context compatibility link without legacy snapshot or path state", () => {
    const href = backupAssetsTaskContextHref(101);

    expect(href).toBe("/app/backups/data?taskId=101");
    expect(href).not.toMatch(/snapshot|path|query|entryId|recoveryPointId/);
    expect(backupAssetsTaskContextHref(0)).toBe("/app/backups/data");
  });
});

function splitHref(href: string): [string, string] {
  const index = href.indexOf("?");
  return index === -1 ? [href, ""] : [href.slice(0, index), href.slice(index)];
}
