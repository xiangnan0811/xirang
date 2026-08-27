import type { BackupAsset, BackupContentProfile, BackupContentRenderer } from "@/types/domain";

export interface BackupAssetPreviewProduct {
  renderer: Exclude<BackupContentRenderer, "attachment">;
  profile: Exclude<BackupContentProfile, "original_v1">;
}

const rasterTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const audioTypes = new Set(["audio/wav", "audio/flac", "audio/ogg", "audio/mpeg"]);
const videoTypes = new Set(["video/mp4", "video/webm", "video/ogg"]);

/** Selects a caller-bound exact product for processing/backcompat flows only. */
export function selectBackupAssetExactPreviewProduct(asset: BackupAsset): BackupAssetPreviewProduct {
  if (asset.entryType !== "file") return { renderer: "metadata_hex", profile: "hex_v1" };

  const mimeType = asset.mimeType.trim().toLowerCase().split(";", 1)[0];
  if (mimeType === "application/pdf") return { renderer: "same_origin_pdf", profile: "pdf_v1" };
  if (rasterTypes.has(mimeType)) return { renderer: "safe_raster", profile: "raster_v1" };
  if (audioTypes.has(mimeType)) return { renderer: "native_audio", profile: "audio_v1" };
  if (videoTypes.has(mimeType)) return { renderer: "native_video", profile: "video_v1" };
  if (isEscapedTextType(mimeType)) return { renderer: "escaped_text", profile: "text_v1" };
  return { renderer: "metadata_hex", profile: "hex_v1" };
}

function isEscapedTextType(mimeType: string): boolean {
  return (
    mimeType.startsWith("text/") ||
    mimeType === "application/json" ||
    mimeType === "application/ld+json" ||
    mimeType === "application/xml" ||
    mimeType.endsWith("+xml") ||
    mimeType === "application/yaml" ||
    mimeType === "application/x-yaml" ||
    mimeType === "application/toml" ||
    mimeType === "image/svg+xml"
  );
}
