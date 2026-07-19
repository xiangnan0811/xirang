import { describe, expect, it } from "vitest";
import { getVisibleNavItems, navItems } from "./navigation";

describe("getVisibleNavItems", () => {
  it("keeps exactly one Backups navigation entry", () => {
    const backupItems = navItems.filter((item) => item.path.startsWith("/app/backups"));
    expect(backupItems).toHaveLength(1);
    expect(backupItems[0]?.path).toBe("/app/backups");
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
