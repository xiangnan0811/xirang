import { describe, expect, it } from "vitest";

import type { BackupAsset, BackupProcessingProduct } from "@/types/domain";

import {
  createBackupAssetsProcessingState,
  processingStateReducer,
  selectProcessingFallback,
  selectProcessingRepresentation,
} from "./backup-assets-processing-state";
import { buildAssetRows } from "./__tests__/test-utils";

function product(overrides: Partial<BackupProcessingProduct> = {}): BackupProcessingProduct {
  return {
    schemaVersion: 1,
    jobId: "1".repeat(32),
    state: "queued",
    representation: "thumbnail",
    capability: "image.thumbnail",
    profile: "raster_thumbnail_v1",
    coverage: null,
    freshness: "current",
    scanStatus: null,
    sensitivityStatus: null,
    reason: null,
    retryable: false,
    fallbackActions: ["native_preview", "download"],
    pollAfterSeconds: 2,
    terminal: false,
    ...overrides,
  };
}

describe("backup asset processing state", () => {
  it("drops stale async completions after an asset revision changes", () => {
    const initial = createBackupAssetsProcessingState(4);
    const loading = processingStateReducer(initial, { type: "loading", revision: 4 });
    const switched = processingStateReducer(loading, { type: "reset", revision: 5 });
    const stale = processingStateReducer(switched, {
      type: "resolved",
      revision: 4,
      products: [product()],
    });
    expect(stale).toEqual(switched);
  });

  it("selects a closed representation from media type without caller tool parameters", () => {
    const base = buildAssetRows(1)[0].asset;
    const cases: Array<[Partial<BackupAsset>, string]> = [
      [{ mimeType: "image/png" }, "thumbnail"],
      [{ mimeType: "video/mp4" }, "media_preview"],
      [{ mimeType: "application/pdf" }, "document_pages"],
      [{ mimeType: "application/zip" }, "archive_index"],
      [{ mimeType: "text/plain" }, "text"],
    ];
    for (const [override, expected] of cases) {
      expect(selectProcessingRepresentation({ ...base, ...override })).toBe(expected);
    }
  });

  it("prefers native preview and never invents an unavailable fallback", () => {
    expect(selectProcessingFallback(product())).toBe("native_preview");
    expect(selectProcessingFallback(product({ fallbackActions: ["download"] }))).toBe("download");
    expect(selectProcessingFallback(product({ fallbackActions: [] }))).toBeNull();
  });
});
