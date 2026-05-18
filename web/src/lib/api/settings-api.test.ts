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
      code: "weak_security_defaults",
      severity: "warning",
      count: 0,
      examples: [],
    });
  });
});
