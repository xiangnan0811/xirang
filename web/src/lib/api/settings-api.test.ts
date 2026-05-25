import { describe, expect, it } from "vitest";
import { mapSecurityRiskSummary } from "./settings-api";

describe("settings api mappers", () => {
  it("maps security risk summary snake_case fields and safe numeric fallbacks", () => {
    const mapped = mapSecurityRiskSummary({
      generated_at: "2026-05-18T00:00:00Z",
      summary: {
        total_risks: "bad",
        categories: "4",
      },
      items: [
        {
          code: "root_ssh_users",
          severity: "critical",
          title: "Root SSH users",
          description: "root nodes",
          count: "2",
          examples: ["node-a", "node-b"],
        },
        {
          code: "broad_scope_ssh_keys",
          severity: "warning",
          title: "Broad scope keys",
          description: "broad keys",
          count: "1",
          examples: ["ops-key"],
        },
        {
          code: "recent_credential_operations",
          severity: "info",
          title: "Recent credential operations",
          description: "recent operations",
          count: "3",
          examples: ["SSH Key export"],
        },
        {
          code: "privileged_users_without_totp",
          severity: "warning",
          title: "Privileged users without 2FA",
          description: "privileged accounts",
          count: "2",
          examples: ["admin（admin）", "operator（operator）"],
        },
        {
          code: "unexpected",
          severity: "unexpected",
          title: "Unknown",
          description: "unknown",
          count: "bad",
          examples: "not-array",
        },
      ],
    });

    expect(mapped.generatedAt).toBe("2026-05-18T00:00:00Z");
    expect(mapped.summary.totalRisks).toBe(0);
    expect(mapped.summary.categories).toBe(4);
    expect(mapped.items[0]).toMatchObject({
      code: "root_ssh_users",
      severity: "critical",
      count: 2,
      examples: ["node-a", "node-b"],
    });
    expect(mapped.items[1]).toMatchObject({
      code: "broad_scope_ssh_keys",
      severity: "warning",
      count: 1,
      examples: ["ops-key"],
    });
    expect(mapped.items[2]).toMatchObject({
      code: "recent_credential_operations",
      severity: "info",
      count: 3,
      examples: ["SSH Key export"],
    });
    expect(mapped.items[3]).toMatchObject({
      code: "privileged_users_without_totp",
      severity: "warning",
      count: 2,
      examples: ["admin（admin）", "operator（operator）"],
    });
    expect(mapped.items[4]).toMatchObject({
      code: "weak_security_defaults",
      severity: "warning",
      count: 0,
      examples: [],
    });
  });
});
