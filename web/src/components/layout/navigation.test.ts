import { describe, expect, it } from "vitest";
import { getVisibleNavItems, navItems } from "./navigation";

  it.each(["operator", "viewer", null] as const)("hides audit for %s", (role) => {
    expect(getVisibleNavItems(role).some((item) => item.path === "/app/audit")).toBe(false);
  });

  it.each([
    ["admin", "/app/backups/data"],
    ["operator", "/app/backups/data"],
    ["viewer", "/app/backups/overview"],
    [null, "/app/backups/overview"],
  ] as const)("routes %s to its permitted backup entry", (role, path) => {
    expect(getVisibleNavItems(role).find((item) => item.titleKey === "nav.backups")?.path).toBe(path);
    expect(navItems.find((item) => item.titleKey === "nav.backups")?.path).toBe("/app/backups/data");
  });

describe("getVisibleNavItems", () => {
  it("does not keep a dashboards navigation entry", () => {
    expect(navItems.some((item) => item.path === "/app/dashboards")).toBe(false);
    expect(getVisibleNavItems("admin").some((item) => item.path === "/app/dashboards")).toBe(false);
  });

  it("keeps exactly one Backups navigation entry", () => {
    const backupItems = navItems.filter((item) => item.path.startsWith("/app/backups"));
    expect(backupItems).toHaveLength(1);
    expect(backupItems[0]?.path).toBe("/app/backups/data");
  });

  it("keeps app credential management admin-only", () => {
    expect(getVisibleNavItems("admin").some((item) => item.path === "/app/credentials")).toBe(true);
    expect(getVisibleNavItems("operator").some((item) => item.path === "/app/credentials")).toBe(false);
    expect(getVisibleNavItems("viewer").some((item) => item.path === "/app/credentials")).toBe(false);
  });

  it("keeps automation rule management admin-only", () => {
    expect(getVisibleNavItems("admin").some((item) => item.path === "/app/automation-rules")).toBe(true);
    expect(getVisibleNavItems("operator").some((item) => item.path === "/app/automation-rules")).toBe(false);
    expect(getVisibleNavItems("viewer").some((item) => item.path === "/app/automation-rules")).toBe(false);
  });

  it("keeps credential audit navigation admin-only", () => {
    expect(getVisibleNavItems("admin").some((item) => item.path === "/app/credential-audit")).toBe(true);
    expect(getVisibleNavItems("operator").some((item) => item.path === "/app/credential-audit")).toBe(false);
    expect(getVisibleNavItems("viewer").some((item) => item.path === "/app/credential-audit")).toBe(false);
  });

  it("keeps credential access grant navigation admin-only", () => {
    expect(getVisibleNavItems("admin").some((item) => item.path === "/app/credential-access-grants")).toBe(true);
    expect(getVisibleNavItems("operator").some((item) => item.path === "/app/credential-access-grants")).toBe(false);
    expect(getVisibleNavItems("viewer").some((item) => item.path === "/app/credential-access-grants")).toBe(false);
  });
});
