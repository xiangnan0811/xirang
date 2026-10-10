import { describe, expect, it } from "vitest";
import type { AlertRecord } from "@/types/domain";
import {
  isQuickSilenceCategory,
  isQuickSilenceNodeId,
  selectQuickSilenceMatch,
  type QuickSilenceSelection,
} from "./alert-silence-match";

type SilenceAlert = Pick<AlertRecord, "nodeId" | "errorCode" | "sloId">;

const QUICK_SILENCE_INSTANCES = [
  ["XR-EXEC", 17],
  ["XR-VRFY", 18],
  ["XR-NODE-EXPIRY", 5],
  ["XR-RETN", 9],
  ["XR-INTG", 4],
  ["XR-DRILL-drill_sandbox_unreachable", 3],
  ["XR-DRILL-drill_verify_failed", 8],
  ["XR-DRILL-drill_restore_failed", 2],
  ["XR-SNAPSHOT-CHURN", 11],
  ["XR-SNAPSHOT-RANSOM", 12],
] as const;

function select(alert: {
  nodeId?: unknown;
  errorCode?: unknown;
  sloId?: unknown;
  nodeName?: unknown;
  message?: unknown;
}): QuickSilenceSelection {
  return selectQuickSilenceMatch(alert as SilenceAlert);
}

describe("selectQuickSilenceMatch", () => {
  it.each(QUICK_SILENCE_INSTANCES)(
    "selects node 42 and category %s from instance %s",
    (category, instance) => {
      expect(select({
        nodeId: 42,
        errorCode: `${category}-${instance}`,
        sloId: null,
      })).toEqual({
        kind: "supported",
        match: { nodeId: 42, category },
      });
    },
  );

  it("keeps a different node and a different category apart", () => {
    expect(select({ nodeId: 42, errorCode: "XR-EXEC-17", sloId: null })).toEqual({
      kind: "supported",
      match: { nodeId: 42, category: "XR-EXEC" },
    });
    expect(select({ nodeId: 43, errorCode: "XR-EXEC-17", sloId: null })).toEqual({
      kind: "supported",
      match: { nodeId: 43, category: "XR-EXEC" },
    });
    expect(select({ nodeId: 42, errorCode: "XR-VRFY-17", sloId: null })).toEqual({
      kind: "supported",
      match: { nodeId: 42, category: "XR-VRFY" },
    });
    expect(select({ nodeId: 17, errorCode: "XR-VRFY-42", sloId: undefined })).toEqual({
      kind: "supported",
      match: { nodeId: 17, category: "XR-VRFY" },
    });
  });

  it("ignores node name and message when the error code is supported", () => {
    expect(select({
      nodeId: 42,
      nodeName: "node-43",
      errorCode: "XR-EXEC-17",
      message: "XR-VRFY-99 on node 7",
      sloId: null,
    })).toEqual({
      kind: "supported",
      match: { nodeId: 42, category: "XR-EXEC" },
    });
  });

  it("accepts a positive safe integer and leading-zero instance digits", () => {
    expect(select({
      nodeId: Number.MAX_SAFE_INTEGER,
      errorCode: "XR-EXEC-017",
      sloId: null,
    })).toEqual({
      kind: "supported",
      match: { nodeId: Number.MAX_SAFE_INTEGER, category: "XR-EXEC" },
    });
  });

  it.each([
    [{ nodeId: 0, errorCode: "XR-EXEC-17", sloId: null }, "platform"],
    [{ nodeId: 0, errorCode: "XR-CRON-DB-BACKUP-abc", sloId: 4 }, "platform"],
    [{ nodeId: -0, errorCode: "XR-STORAGE-LOW:/data", sloId: null }, "platform"],
    [{ nodeId: -1, errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: -42, errorCode: "XR-EXEC-17", sloId: 9 }, "unknown-node"],
    [{ nodeId: 42.5, errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: Number.NaN, errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: Number.POSITIVE_INFINITY, errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: Number.MAX_SAFE_INTEGER + 1, errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: null, errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: "42", errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: "0", errorCode: "XR-EXEC-17", sloId: null }, "unknown-node"],
    [{ nodeId: 42, errorCode: "XR-EXEC-17", sloId: 1 }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-EXEC-17", sloId: 0 }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-SLO-1", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-NODE-123", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-NODE-1", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-SERVICE-DOWN-123", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-EXEC", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-EXEC-extra-17", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-EXEC-17-extra", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-EXEC-17 ", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-EXEC-17\n", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: " XR-EXEC-17", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "xr-exec-17", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-CRON-DB-BACKUP-abc", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-STORAGE-LOW:/data", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: "XR-EXEC-acked", sloId: null }, "unsupported-source"],
    [{ nodeId: 42, sloId: null }, "unsupported-source"],
    [{ nodeId: 42, errorCode: 17, sloId: null }, "unsupported-source"],
  ] as const)("rejects %j as %s without a wildcard match", (alert, reason) => {
    const result = select(alert);
    expect(result).toEqual({ kind: "unsupported", reason });
    expect(result).not.toHaveProperty("match");
    expect(JSON.stringify(result)).not.toContain('"category":""');
    expect(JSON.stringify(result)).not.toContain('"nodeId":null');
  });
});

describe("quick silence validators", () => {
  it.each(QUICK_SILENCE_INSTANCES.map(([category]) => category))(
    "accepts category %s",
    (category) => {
      expect(isQuickSilenceCategory(category)).toBe(true);
    },
  );

  it.each([
    "",
    "XR-EXEC-17",
    "XR-NODE",
    "XR-NODE-EXPIRY-5",
    "XR-DRILL",
    "XR-SNAPSHOT",
    "xr-exec",
  ])("rejects category %j", (category) => {
    expect(isQuickSilenceCategory(category)).toBe(false);
  });

  it("rejects a non-string category", () => {
    expect(isQuickSilenceCategory(undefined)).toBe(false);
    expect(isQuickSilenceCategory(null)).toBe(false);
  });

  it("accepts only a positive safe integer node id", () => {
    expect(isQuickSilenceNodeId(1)).toBe(true);
    expect(isQuickSilenceNodeId(42)).toBe(true);
    expect(isQuickSilenceNodeId(Number.MAX_SAFE_INTEGER)).toBe(true);
    expect(isQuickSilenceNodeId(0)).toBe(false);
    expect(isQuickSilenceNodeId(-1)).toBe(false);
    expect(isQuickSilenceNodeId(42.5)).toBe(false);
    expect(isQuickSilenceNodeId(Number.NaN)).toBe(false);
    expect(isQuickSilenceNodeId(Number.MAX_SAFE_INTEGER + 1)).toBe(false);
    expect(isQuickSilenceNodeId(undefined)).toBe(false);
    expect(isQuickSilenceNodeId("42")).toBe(false);
  });
});
