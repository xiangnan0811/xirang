import { describe, expect, it } from "vitest";
import {
  blockedBackupAssetProjection,
  mapAssetRef,
  mapOpaqueBackupAssetId,
  mapSafeNonNegativeInteger,
  mapUTCInstant,
} from "./backup-assets-boundary";

describe("backup assets shared API boundary", () => {
  it("maps opaque IDs, composite refs, safe integers, and UTC instants", () => {
    const point = "1".repeat(32);
    const entry = "a".repeat(64);
    expect(mapOpaqueBackupAssetId(point)).toBe(point);
    expect(mapAssetRef({ recovery_point_id: point, entry_id: entry })).toEqual({
      recoveryPointId: point,
      entryId: entry,
    });
    expect(mapSafeNonNegativeInteger(42)).toBe(42);
    expect(mapUTCInstant("2026-07-18T08:00:00+08:00")).toBe("2026-07-18T00:00:00.000Z");
  });

  it("fails closed for malformed shared values", () => {
    expect(mapOpaqueBackupAssetId("latest")).toBeNull();
    expect(mapAssetRef({ recovery_point_id: "bad", entry_id: "bad" })).toBeNull();
    expect(mapSafeNonNegativeInteger(Number.MAX_SAFE_INTEGER + 1)).toBeNull();
    expect(mapUTCInstant("not-a-time")).toBeNull();
    expect(blockedBackupAssetProjection()).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
  });
});
