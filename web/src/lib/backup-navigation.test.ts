import { describe, expect, it } from "vitest";

import { parseBackupAssetsRoute } from "@/features/backup-assets/backup-assets-route-state";
import {
  backupFilesHref,
  canonicalizeBackupLocation,
  getBackupActivePage,
  isNavPathActive,
  normalizeAppPathname,
} from "./backup-navigation";

const savedSearchId = "e".repeat(32);
const filesSavedSearch = `?view=search&savedSearchId=${savedSearchId}`;

describe("normalizeAppPathname", () => {
  it("strips trailing slashes without collapsing the root", () => {
    expect(normalizeAppPathname("/")).toBe("/");
    expect(normalizeAppPathname("/app/backups/")).toBe("/app/backups");
    expect(normalizeAppPathname("/app/backups/data///")).toBe("/app/backups/data");
  });
});

describe("isNavPathActive", () => {
  it("keeps nested and trailing backup paths on the Backups item", () => {
    expect(isNavPathActive("/app/backups/data", "/app/backups")).toBe(true);
    expect(isNavPathActive("/app/backups/data", "/app/backups/")).toBe(true);
    expect(isNavPathActive("/app/backups/data", "/app/backups/data")).toBe(true);
    expect(isNavPathActive("/app/backups/data", "/app/backups/data/")).toBe(true);
    expect(isNavPathActive("/app/backups/data", "/app/backups/overview")).toBe(true);
    expect(isNavPathActive("/app/backups/data", "/app/backups/overview/")).toBe(true);
    expect(isNavPathActive("/app/backups/data", "/app/backups/recovery")).toBe(true);
    expect(isNavPathActive("/app/backups/data", "/app/backups/recovery/")).toBe(true);
  });

  it("does not treat Tasks or Overview as backup nested matches", () => {
    expect(isNavPathActive("/app/tasks", "/app/backups")).toBe(false);
    expect(isNavPathActive("/app/tasks", "/app/backups/data")).toBe(false);
    expect(isNavPathActive("/app/overview", "/app/backups/overview")).toBe(false);
    expect(isNavPathActive("/app/tasks", "/app/tasks/")).toBe(true);
  });

  it("rejects false prefix siblings of the Backups section", () => {
    expect(isNavPathActive("/app/backups/data", "/app/backups-data")).toBe(false);
    expect(isNavPathActive("/app/backups/data", "/app/backups/data-room")).toBe(false);
    expect(isNavPathActive("/app/backups/data", "/app/backups/database")).toBe(false);
    expect(isNavPathActive("/app/backups/data", "/app/backup")).toBe(false);
    expect(isNavPathActive("/app/backups/data", "/app/overview")).toBe(false);
  });
});

describe("backupFilesHref", () => {
  it("keeps exact Files search only on a normalized Files route", () => {
    expect(parseBackupAssetsRoute("/app/backups/data", filesSavedSearch)).toMatchObject({
      status: "valid",
      state: { page: "data", view: "search", savedSearchId },
    });
    expect(backupFilesHref("/app/backups/data", filesSavedSearch)).toBe(
      `/app/backups/data${filesSavedSearch}`,
    );
    expect(backupFilesHref("/app/backups/data/", "?taskId=7")).toBe("/app/backups/data?taskId=7");
    expect(backupFilesHref("/app/backups/data", "")).toBe("/app/backups/data");
  });

  it("never copies Overview, Recovery, index, or unrelated search onto Files", () => {
    expect(backupFilesHref("/app/backups/overview", "?foo=1")).toBe("/app/backups/data");
    expect(backupFilesHref("/app/backups/overview/", "?foo=1")).toBe("/app/backups/data");
    expect(backupFilesHref("/app/backups/recovery", "?planId=abc")).toBe("/app/backups/data");
    expect(backupFilesHref("/app/backups", "?taskId=7")).toBe("/app/backups/data");
    expect(backupFilesHref("/app/tasks", "?taskId=7")).toBe("/app/backups/data");
  });
});

describe("getBackupActivePage", () => {
  it("uses Files as the active fallback including the index and trailing slashes", () => {
    expect(getBackupActivePage("/app/backups")).toBe("data");
    expect(getBackupActivePage("/app/backups/")).toBe("data");
    expect(getBackupActivePage("/app/backups/data")).toBe("data");
    expect(getBackupActivePage("/app/backups/data/")).toBe("data");
    expect(getBackupActivePage("/app/backups/overview")).toBe("overview");
    expect(getBackupActivePage("/app/backups/overview/")).toBe("overview");
    expect(getBackupActivePage("/app/backups/recovery")).toBe("recovery");
    expect(getBackupActivePage("/app/backups/recovery/")).toBe("recovery");
  });
});

describe("canonicalizeBackupLocation", () => {
  it("redirects the production backup index before UI mount", () => {
    const result = canonicalizeBackupLocation({
      request: new Request("http://localhost/app/backups"),
    });
    expect(result).toBeInstanceOf(Response);
    expect((result as Response).headers.get("Location")).toBe("/app/backups/data");
    expect((result as Response).status).toBe(302);
  });

  it("redirects a trailing-slash index and known tab without dropping search", () => {
    const index = canonicalizeBackupLocation({
      request: new Request("http://localhost/app/backups/?taskId=7"),
    });
    expect((index as Response).headers.get("Location")).toBe("/app/backups/data?taskId=7");

    const data = canonicalizeBackupLocation({
      request: new Request(`http://localhost/app/backups/data/${filesSavedSearch}`),
    });
    expect((data as Response).headers.get("Location")).toBe(
      `/app/backups/data${filesSavedSearch}`,
    );
  });

  it("leaves canonical nested backup pages to their route handlers", () => {
    expect(
      canonicalizeBackupLocation({
        request: new Request("http://localhost/app/backups/overview"),
      }),
    ).toBeNull();
    expect(
      canonicalizeBackupLocation({
        request: new Request(`http://localhost/app/backups/data${filesSavedSearch}`),
      }),
    ).toBeNull();
  });
});
