import { describe, expect, it } from "vitest";
import type { CatalogCapabilityCode } from "@/types/domain";

import {
  getBackupAssetsResultSurface,
  getPreviewAccess,
  presentBackupAssetsCode,
} from "./backup-assets-presenters";

describe("backup asset presenters", () => {
  it.each([
    [{ coverage: "complete", count: 0, authoritativeEmpty: true, offline: false }, "empty"],
    [{ coverage: "partial", count: 0, authoritativeEmpty: false, offline: false }, "partial"],
    [{ coverage: "building", count: 0, authoritativeEmpty: false, offline: false }, "loading"],
    [{ coverage: "complete", count: 2, authoritativeEmpty: false, offline: true }, "rows_offline"],
    [{ coverage: "unavailable", count: 0, authoritativeEmpty: false, offline: true }, "unavailable"],
  ] as const)("keeps coverage/offline/empty products orthogonal", (input, expected) => {
    expect(getBackupAssetsResultSurface(input)).toBe(expected);
  });

  it("does not call partial zero an empty result", () => {
    expect(
      getBackupAssetsResultSurface({
        coverage: "partial",
        count: 0,
        authoritativeEmpty: true,
        offline: false,
      })
    ).toBe("partial");
  });

  it.each([
    ["non_secret", false, "allowed"],
    ["non_secret", true, "blocked_unnecessary_proof"],
    ["secret", false, "step_up_required"],
    ["unknown", false, "step_up_required"],
    ["secret", true, "allowed"],
    ["future", true, "blocked_unknown"],
  ] as const)("fails closed for preview classification %s", (classification, proofPresent, expected) => {
    expect(getPreviewAccess(classification, proofPresent)).toBe(expected);
  });

  it("maps future provider/state codes to one localized fallback without echoing them", () => {
    expect(presentBackupAssetsCode("provider", "future_raw_/private/path")).toEqual({
      translationKey: "backupAssets.codes.unknown",
      tone: "warning",
    });
    expect(JSON.stringify(presentBackupAssetsCode("provider", "future_raw_/private/path"))).not.toContain(
      "future_raw"
    );
  });

  it.each([
    "feature_disabled",
    "task_artifact_contract_missing",
    "repository_offline",
    "repository_disconnected",
    "provider_unavailable",
    "repository_identity_unavailable",
    "provider_protocol_incompatible",
    "provider_operation_timeout",
    "provider_resource_limit",
    "point_not_committed",
    "mutable_source_changed",
    "catalog_unavailable",
    "sequential_read_unavailable",
    "range_unavailable",
    "download_unavailable",
    "restore_unavailable",
    "diff_unavailable",
    "unknown_internal_state",
  ] satisfies CatalogCapabilityCode[])("maps the closed capability code %s to a dedicated localized key", (code) => {
    expect(presentBackupAssetsCode("capability", code).translationKey).not.toBe("backupAssets.codes.unknown");
  });
});
