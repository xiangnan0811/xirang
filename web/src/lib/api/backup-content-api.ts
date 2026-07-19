import type {
  AssetRef,
  BackupContentAction,
  BackupContentClassification,
  BackupContentProfile,
  BackupContentRangePolicy,
  BackupContentRenderer,
  BackupContentTicket,
  CatalogProjection,
} from "@/types/domain";
import {
  blockedBackupAssetProjection,
  isRawBackupAssetObject,
  mapBackupAssetEntryId,
  mapOpaqueBackupAssetId,
  mapSafeNonNegativeInteger,
  mapUTCInstant,
  type RawBackupAssetObject,
} from "./backup-assets-boundary";
import { request } from "./core";

const actions = new Set<BackupContentAction>(["preview", "download"]);
const renderers = new Set<BackupContentRenderer>([
  "escaped_text",
  "safe_raster",
  "same_origin_pdf",
  "native_audio",
  "native_video",
  "metadata_hex",
  "attachment",
]);
const profiles = new Set<BackupContentProfile>([
  "text_v1",
  "raster_v1",
  "pdf_v1",
  "audio_v1",
  "video_v1",
  "hex_v1",
  "original_v1",
]);
const ranges = new Set<BackupContentRangePolicy>(["none", "single"]);
const classifications = new Set<BackupContentClassification>(["non_secret", "secret", "unknown"]);

const rendererProfiles: Record<BackupContentRenderer, BackupContentProfile> = {
  escaped_text: "text_v1",
  safe_raster: "raster_v1",
  same_origin_pdf: "pdf_v1",
  native_audio: "audio_v1",
  native_video: "video_v1",
  metadata_hex: "hex_v1",
  attachment: "original_v1",
};

const rendererContentTypes: Record<BackupContentRenderer, ReadonlySet<string>> = {
  escaped_text: new Set(["text/plain; charset=utf-8"]),
  safe_raster: new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]),
  same_origin_pdf: new Set(["application/pdf"]),
  native_audio: new Set(["audio/wav", "audio/flac", "audio/ogg", "audio/mpeg"]),
  native_video: new Set(["video/mp4", "video/webm", "video/ogg"]),
  metadata_hex: new Set(["text/plain; charset=utf-8"]),
  attachment: new Set(["application/octet-stream"]),
};

export interface BackupContentTicketInput {
  schemaVersion: 1;
  action: BackupContentAction;
  renderer: BackupContentRenderer;
  profile: BackupContentProfile;
  stepUpProof?: string;
  signal?: AbortSignal;
}

function closedValue<T extends string>(value: unknown, accepted: Set<T>): T | null {
  return typeof value === "string" && accepted.has(value as T) ? value as T : null;
}

function validStepUpProof(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 8192 && !/[\r\n\0\s]/.test(value);
}

function validRequestedProduct(input: BackupContentTicketInput): boolean {
  if (input.schemaVersion !== 1 || !actions.has(input.action) || !renderers.has(input.renderer) ||
      !profiles.has(input.profile) || rendererProfiles[input.renderer] !== input.profile ||
      (input.stepUpProof !== undefined && !validStepUpProof(input.stepUpProof))) {
    return false;
  }
  if (input.action === "download") {
    return input.renderer === "attachment" && input.profile === "original_v1" && validStepUpProof(input.stepUpProof);
  }
  return input.renderer !== "attachment";
}

function validResponseProduct(
  action: BackupContentAction,
  renderer: BackupContentRenderer,
  profile: BackupContentProfile,
  range: BackupContentRangePolicy,
): boolean {
  if (rendererProfiles[renderer] !== profile) return false;
  if ((renderer === "escaped_text" || renderer === "metadata_hex") && range !== "none") return false;
  return action === "download"
    ? renderer === "attachment" && profile === "original_v1"
    : renderer !== "attachment";
}

function mapRFC3339Instant(value: unknown): string | null {
  if (typeof value !== "string" ||
      !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value)) {
    return null;
  }
  return mapUTCInstant(value);
}

function validContentURL(value: unknown): value is string {
  return typeof value === "string" && /^\/api\/v1\/asset-content\/[0-9a-f]{32}$/.test(value);
}

function validEntityTag(value: unknown): value is string {
  return typeof value === "string" && /^(?:W\/)?"[A-Za-z0-9._~:-]{1,128}"$/.test(value);
}

function blocked(): CatalogProjection<BackupContentTicket> {
  return blockedBackupAssetProjection();
}

export function mapBackupContentTicket(
  value: unknown,
  expected: BackupContentTicketInput,
): CatalogProjection<BackupContentTicket> {
  if (!validRequestedProduct(expected) || !isRawBackupAssetObject(value) || value.schema_version !== 1 ||
      !validContentURL(value.content_url) || !Array.isArray(value.fallback_actions) ||
      value.fallback_actions.length !== 0 || value.capability_reason !== null) {
    return blocked();
  }

  const action = closedValue(value.action, actions);
  const renderer = closedValue(value.renderer, renderers);
  const profile = closedValue(value.profile, profiles);
  const range = closedValue(value.range, ranges);
  const classification = closedValue(value.classification, classifications);
  const contentLength = mapSafeNonNegativeInteger(value.content_length);
  const lastModified = value.last_modified === null ? null : mapRFC3339Instant(value.last_modified);
  const expiresAt = mapRFC3339Instant(value.expires_at);
  const idleExpiresAt = mapRFC3339Instant(value.idle_expires_at);

  if (action === null || renderer === null || profile === null || range === null || classification === null ||
      contentLength === null || (value.last_modified !== null && lastModified === null) ||
      expiresAt === null || idleExpiresAt === null || !validEntityTag(value.etag) ||
      typeof value.content_type !== "string" || !rendererContentTypes[renderer].has(value.content_type) ||
      action !== expected.action || renderer !== expected.renderer || profile !== expected.profile ||
      !validResponseProduct(action, renderer, profile, range) ||
      Date.parse(expiresAt) <= Date.now() || Date.parse(idleExpiresAt) <= Date.now() || idleExpiresAt > expiresAt) {
    return blocked();
  }

  const proofPresent = validStepUpProof(expected.stepUpProof);
  if ((action === "download" && !proofPresent) ||
      (action === "preview" && classification === "non_secret" && proofPresent) ||
      (action === "preview" && classification !== "non_secret" && !proofPresent)) {
    return blocked();
  }

  const fallbackActions: BackupContentAction[] = [];
  return {
    status: "available",
    value: {
      schemaVersion: 1,
      contentUrl: value.content_url,
      action,
      renderer,
      profile,
      contentType: value.content_type,
      contentLength,
      etag: value.etag,
      lastModified,
      range,
      classification,
      expiresAt,
      idleExpiresAt,
      capabilityReason: null,
      fallbackActions,
    },
  };
}

function validAssetRef(ref: AssetRef): boolean {
  return mapOpaqueBackupAssetId(ref.recoveryPointId) !== null && mapBackupAssetEntryId(ref.entryId) !== null;
}

function encodeTicketInput(input: BackupContentTicketInput): RawBackupAssetObject {
  return {
    schema_version: 1,
    action: input.action,
    renderer: input.renderer,
    profile: input.profile,
  };
}

export function createBackupContentApi() {
  return {
    async issueTicket(
      token: string,
      ref: AssetRef,
      input: BackupContentTicketInput,
    ): Promise<CatalogProjection<BackupContentTicket>> {
      if (!validAssetRef(ref) || !validRequestedProduct(input)) {
        throw new Error("invalid backup content ticket request");
      }
      const raw = await request<unknown>(
        `/recovery-points/${ref.recoveryPointId}/entries/${ref.entryId}/delivery-tickets`,
        {
          method: "POST",
          token,
          stepUpProof: input.stepUpProof,
          signal: input.signal,
          body: encodeTicketInput(input),
        },
      );
      return mapBackupContentTicket(raw, input);
    },
  };
}
