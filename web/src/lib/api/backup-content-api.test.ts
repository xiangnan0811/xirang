import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AssetRef } from "@/types/domain";
import { request } from "./core";
import {
  createBackupContentApi,
  mapBackupContentTicket,
  type BackupContentTicketInput,
} from "./backup-content-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return { ...actual, request: vi.fn() };
});

const requestMock = vi.mocked(request);
const ref: AssetRef = {
  recoveryPointId: "1".repeat(32),
  entryId: "a".repeat(64),
};
const previewInput: BackupContentTicketInput = {
  schemaVersion: 1,
  action: "preview",
  renderer: "safe_raster",
  profile: "raster_v1",
};

function rawTicket() {
  return {
    schema_version: 1,
    content_url: `/api/v1/asset-content/${"d".repeat(32)}`,
    action: "preview",
    renderer: "safe_raster",
    profile: "raster_v1",
    content_type: "image/png",
    content_length: 12345,
    etag: `W/"${"e".repeat(64)}"`,
    last_modified: "2026-07-18T08:00:00+08:00",
    range: "single",
    classification: "non_secret",
    expires_at: "2026-07-18T00:02:00Z",
    idle_expires_at: "2026-07-18T00:01:00Z",
    capability_reason: null,
    fallback_actions: [],
    delivery_id: "PRIVATE DELIVERY ID",
    cookie_secret: "PRIVATE COOKIE SECRET",
  };
}

function expectBlocked(value: ReturnType<typeof mapBackupContentTicket>): void {
  expect(value).toEqual({
    status: "blocked",
    reason: { code: "unknown_internal_state", params: {} },
  });
}

describe("backup content API boundary", () => {
  beforeEach(() => {
    requestMock.mockReset();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-18T00:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("maps one closed ticket atomically and drops private raw fields", () => {
    const mapped = mapBackupContentTicket(rawTicket(), previewInput);
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") throw new Error("expected available ticket");
    expect(mapped.value).toEqual({
      schemaVersion: 1,
      contentUrl: `/api/v1/asset-content/${"d".repeat(32)}`,
      action: "preview",
      renderer: "safe_raster",
      profile: "raster_v1",
      contentType: "image/png",
      contentLength: 12345,
      etag: `W/"${"e".repeat(64)}"`,
      lastModified: "2026-07-18T00:00:00.000Z",
      range: "single",
      classification: "non_secret",
      expiresAt: "2026-07-18T00:02:00.000Z",
      idleExpiresAt: "2026-07-18T00:01:00.000Z",
      capabilityReason: null,
      fallbackActions: [],
    });
    expect(JSON.stringify(mapped)).not.toContain("PRIVATE");
    expect(JSON.stringify(mapped)).not.toContain("delivery_id");
    expect(JSON.stringify(mapped)).not.toContain("cookie_secret");
  });

  it.each([
    ["unknown schema", { schema_version: 2 }],
    ["absolute URL", { content_url: `https://evil.example/api/v1/asset-content/${"d".repeat(32)}` }],
    ["query URL", { content_url: `/api/v1/asset-content/${"d".repeat(32)}?jwt=bad` }],
    ["fragment URL", { content_url: `/api/v1/asset-content/${"d".repeat(32)}#secret` }],
    ["unknown action", { action: "restore" }],
    ["profile mismatch", { profile: "text_v1" }],
    ["text Range mismatch", { renderer: "escaped_text", profile: "text_v1", content_type: "text/plain; charset=utf-8", range: "single" }],
    ["MIME mismatch", { content_type: "text/html" }],
    ["unknown range", { range: "multipart" }],
    ["unknown classification", { classification: "public" }],
    ["unsafe ETag", { etag: "opaque-unquoted" }],
    ["unsafe length", { content_length: Number.MAX_SAFE_INTEGER + 1 }],
    ["invalid modified time", { last_modified: "not-a-time" }],
    ["expired ticket", { expires_at: "2026-07-17T23:59:59Z" }],
    ["idle after absolute", { idle_expires_at: "2026-07-18T00:03:00Z" }],
    ["capability contradiction", { capability_reason: { code: "range_unavailable", params: {} } }],
    ["fallback contradiction", { fallback_actions: ["download"] }],
  ])("blocks the whole projection for %s", (_name, override) => {
    expectBlocked(mapBackupContentTicket({ ...rawTicket(), ...override }, previewInput));
  });

  it("couples classification and action to exact proof presence", () => {
    const revealInput: BackupContentTicketInput = { ...previewInput, stepUpProof: "reveal-proof" };
    expect(mapBackupContentTicket({ ...rawTicket(), classification: "secret" }, revealInput).status).toBe("available");
    expectBlocked(mapBackupContentTicket({ ...rawTicket(), classification: "secret" }, previewInput));
    expectBlocked(mapBackupContentTicket(rawTicket(), revealInput));

    const downloadInput: BackupContentTicketInput = {
      schemaVersion: 1,
      action: "download",
      renderer: "attachment",
      profile: "original_v1",
      stepUpProof: "download-proof",
    };
    const download = {
      ...rawTicket(),
      action: "download",
      renderer: "attachment",
      profile: "original_v1",
      content_type: "application/octet-stream",
      etag: `"${"f".repeat(64)}"`,
    };
    expect(mapBackupContentTicket(download, downloadInput).status).toBe("available");
    expectBlocked(mapBackupContentTicket(download, { ...downloadInput, stepUpProof: undefined }));
  });

  it("posts the exact body and forwards the proof only through request options", async () => {
    const input: BackupContentTicketInput = { ...previewInput, stepUpProof: "reveal-proof" };
    requestMock.mockResolvedValueOnce({ ...rawTicket(), classification: "unknown" });

    const mapped = await createBackupContentApi().issueTicket("login-token", ref, input);

    expect(mapped.status).toBe("available");
    expect(requestMock).toHaveBeenCalledWith(
      `/recovery-points/${ref.recoveryPointId}/entries/${ref.entryId}/delivery-tickets`,
      {
        method: "POST",
        token: "login-token",
        stepUpProof: "reveal-proof",
        signal: undefined,
        body: {
          schema_version: 1,
          action: "preview",
          renderer: "safe_raster",
          profile: "raster_v1",
        },
      },
    );
    expect(JSON.stringify(requestMock.mock.calls)).not.toContain("?jwt=");
  });

  it("rejects malformed refs and illegal request products before transport", async () => {
    await expect(createBackupContentApi().issueTicket("token", { ...ref, entryId: "latest" }, previewInput)).rejects.toThrow();
    await expect(createBackupContentApi().issueTicket("token", ref, {
      schemaVersion: 1,
      action: "download",
      renderer: "safe_raster",
      profile: "raster_v1",
      stepUpProof: "download-proof",
    })).rejects.toThrow();
    expect(requestMock).not.toHaveBeenCalled();
  });
});

describe("backup content browser-state boundary", () => {
  it("keeps the content URL opaque and out of browser persistence primitives", async () => {
    const source = await import("./backup-content-api?raw");
    const text = String(source.default);
    for (const forbidden of [
      "localStorage",
      "sessionStorage",
      "history",
      "location",
      "router",
      "document.cookie",
      "createObjectURL",
      "new Blob",
      "fetch(",
      "delivery_id",
      "cookie_secret",
    ]) {
      expect(text).not.toContain(forbidden);
    }
  });
});
