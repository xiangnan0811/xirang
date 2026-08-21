import { describe, expect, it } from "vitest";

import { ApiError } from "./core";
import { mapBackupAssetsError } from "./backup-assets-error";

function capabilityError(status: number, code: string) {
  return new ApiError(status, "raw provider error /private/path", {
    code: status,
    message: "raw provider error /private/path",
    data: { reason: { code, params: {} }, correlation_id: "not-for-ui" },
  });
}

describe("mapBackupAssetsError", () => {
  it("maps a secret-reveal 403 without treating it as generic permission denial", () => {
    expect(
      mapBackupAssetsError(
        new ApiError(403, "需要二次验证", {
          code: 403,
          message: "需要二次验证",
          data: { reason: { code: "secret_reveal_required", params: {} } },
        }),
        "content_ticket"
      )
    ).toEqual({
      code: "secret_reveal_required",
      translationKey: "backupAssets.errors.secretRevealRequired",
      retryable: false,
      action: "none",
    });
  });

  it("maps a verified feature-disabled capability without exposing detail", () => {
    expect(mapBackupAssetsError(capabilityError(503, "feature_disabled"), "repositories")).toEqual({
      code: "feature_disabled",
      translationKey: "backupAssets.errors.featureDisabled",
      retryable: false,
      action: "return_overview",
      capabilityCode: "feature_disabled",
    });
  });

  it("preserves a known non-secret capability code as a closed unavailable state", () => {
    expect(mapBackupAssetsError(capabilityError(503, "repository_offline"), "content_ticket")).toEqual({
      code: "temporarily_unavailable",
      translationKey: "backupAssets.errors.temporarilyUnavailable",
      retryable: true,
      action: "retry",
      capabilityCode: "repository_offline",
    });
  });

  it.each([
    [new ApiError(403, "raw forbidden", null), "directory", "permission_denied", "none"],
    [new ApiError(404, "raw missing", null), "entry", "not_found", "return_context"],
    [new ApiError(400, "raw invalid", null), "search", "invalid_request", "none"],
    [new ApiError(501, "raw unsupported", null), "content_ticket", "unsupported", "none"],
  ] as const)("maps status-only errors safely", (error, context, code, action) => {
    expect(mapBackupAssetsError(error, context)).toMatchObject({ code, action });
    expect(JSON.stringify(mapBackupAssetsError(error, context))).not.toContain(error.message);
  });

  it("distinguishes cursor refresh from overlay conflict only by caller context", () => {
    const error = new ApiError(409, "raw stale /private/path", null);
    expect(mapBackupAssetsError(error, "cursor")).toMatchObject({
      code: "stale_cursor",
      action: "refresh_first_page",
      retryable: true,
    });
    expect(mapBackupAssetsError(error, "overlay_mutation")).toMatchObject({
      code: "conflict",
      action: "refetch",
      retryable: false,
    });
  });

  it("keeps only a finite bounded retry-after value", () => {
    expect(mapBackupAssetsError(new ApiError(429, "raw", null, 12), "search")).toEqual({
      code: "rate_limited",
      translationKey: "backupAssets.errors.rateLimited",
      retryable: true,
      action: "retry",
      retryAfter: 12,
    });
    expect(mapBackupAssetsError(new ApiError(429, "raw", null, Number.POSITIVE_INFINITY), "search"))
      .not.toHaveProperty("retryAfter");
  });

  it.each([
    { code: 503, data: { reason: { code: "future_raw_/private/path", params: {} } } },
    { code: 503, data: { reason: { code: "feature_disabled", params: { secret: "x".repeat(5000) } } } },
    { code: 503, data: { reason: { code: "feature_disabled", params: [] } } },
    "x".repeat(5000),
  ])("fails closed for malformed or oversized detail", (detail) => {
    const result = mapBackupAssetsError(new ApiError(503, "raw /private/path", detail), "repositories");
    expect(result).toEqual({
      code: "temporarily_unavailable",
      translationKey: "backupAssets.errors.temporarilyUnavailable",
      retryable: true,
      action: "retry",
    });
    expect(JSON.stringify(result)).not.toMatch(/private|future_raw|secret/);
  });

  it("maps non-ApiError input to one safe unknown product", () => {
    expect(mapBackupAssetsError(new Error("tool output /secret/path"), "diff")).toEqual({
      code: "unknown",
      translationKey: "backupAssets.errors.unknown",
      retryable: false,
      action: "none",
    });
  });
});
