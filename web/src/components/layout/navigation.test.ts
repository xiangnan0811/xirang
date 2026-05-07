import { describe, expect, it } from "vitest";
import { getVisibleNavItems } from "./navigation";

describe("getVisibleNavItems", () => {
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
});
