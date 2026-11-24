import { describe, expect, it } from "vitest";
import { deriveOverview } from "./use-console-data.utils";
import type { PolicyRecord } from "@/types/domain";

function makePolicy(id: number, enabled: boolean, isTemplate = false): PolicyRecord {
  return {
    id,
    name: `policy-${id}`,
    sourcePath: "/src",
    targetPath: "/dst",
    cron: "0 2 * * *",
    naturalLanguage: "daily",
    enabled,
    isTemplate,
    criticalThreshold: 2,
    nodeIds: [],
    verifyEnabled: true,
    verifySampleRate: 0,
    drillEnabled: false,
    drillCron: "",
    drillRestorePath: "/tmp/xirang-drill",
    drillPreVerify: "",
    drillVerify: "",
    drillPostVerify: "",
    drillAutoCleanup: true,
  };
}

describe("deriveOverview", () => {
  it("excludes enabled template policies from client fallback count to match server /overview", () => {
    const policies = [
      makePolicy(1, true, false),
      makePolicy(2, true, true),
      makePolicy(3, false, false),
    ];

    expect(deriveOverview(policies, null, true)).toEqual({ activePolicies: 1 });
    expect(deriveOverview([], null, true)).toEqual({ activePolicies: 0 });
    expect(deriveOverview(policies, { activePolicies: 4 }, false)).toEqual({ activePolicies: 4 });
  });

  it("returns null instead of fabricating 0 when summary is absent and policies are not loaded yet", () => {
    expect(deriveOverview([], null, false)).toEqual({ activePolicies: null });
    expect(deriveOverview([], undefined, false)).toEqual({ activePolicies: null });
  });
});
