import type {
  BackupAsset,
  BackupProcessingFallbackAction,
  BackupProcessingProduct,
  BackupProcessingRepresentation,
} from "@/types/domain";

export type BackupAssetsProcessingState = {
  revision: number;
  status: "idle" | "loading" | "ready" | "error";
  products: BackupProcessingProduct[];
  active: BackupProcessingProduct | null;
  error: Error | null;
};

export type BackupAssetsProcessingAction =
  | { type: "reset"; revision: number }
  | { type: "loading"; revision: number }
  | { type: "resolved"; revision: number; products: BackupProcessingProduct[]; active?: BackupProcessingProduct | null }
  | { type: "failed"; revision: number; error: Error };

export function createBackupAssetsProcessingState(revision = 0): BackupAssetsProcessingState {
  return { revision, status: "idle", products: [], active: null, error: null };
}

export function processingStateReducer(
  state: BackupAssetsProcessingState,
  action: BackupAssetsProcessingAction
): BackupAssetsProcessingState {
  if (action.type !== "reset" && action.revision !== state.revision) return state;
  switch (action.type) {
    case "reset":
      return createBackupAssetsProcessingState(action.revision);
    case "loading":
      return { ...state, status: "loading", error: null };
    case "resolved":
      return {
        ...state,
        status: "ready",
        products: [...action.products],
        active: action.active === undefined ? state.active : action.active,
        error: null,
      };
    case "failed":
      return { ...state, status: "error", error: action.error };
  }
}

export function selectProcessingRepresentation(asset: BackupAsset): BackupProcessingRepresentation {
  const mediaType = asset.mimeType.toLowerCase();
  if (mediaType.startsWith("image/")) return "thumbnail";
  if (mediaType.startsWith("audio/") || mediaType.startsWith("video/")) return "media_preview";
  if (mediaType === "application/pdf" || mediaType.includes("officedocument") || mediaType.includes("opendocument")) {
    return "document_pages";
  }
  if (mediaType === "application/zip" || mediaType.includes("tar") || mediaType.includes("gzip") || mediaType.includes("zstd")) {
    return "archive_index";
  }
  return "text";
}

export function selectProcessingFallback(product: BackupProcessingProduct): BackupProcessingFallbackAction | null {
  if (product.fallbackActions.includes("native_preview")) return "native_preview";
  if (product.fallbackActions.includes("download")) return "download";
  return null;
}
