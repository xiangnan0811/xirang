import { describe, expect, it, vi } from "vitest";

import type { AssetRef } from "@/types/domain";

import {
  createBackupAssetProcessingApi,
  mapProcessingAdminControl,
  mapAssetProcessingState,
  mapProcessingProduct,
  mapProcessingUpdaterCandidates,
} from "./backup-asset-processing-api";

const ref: AssetRef = {
  recoveryPointId: "1".repeat(32),
  entryId: "2".repeat(64),
};
const jobId = "3".repeat(32);

function queuedProduct(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: 1,
    job_id: jobId,
    state: "queued",
    representation: "thumbnail",
    capability: "image.thumbnail",
    profile: "raster_thumbnail_v1",
    freshness: "current",
    retryable: false,
    fallback_actions: ["native_preview", "download"],
    poll_after_seconds: 2,
    terminal: false,
    ...overrides,
  };
}

describe("backup asset processing boundary", () => {
  it("maps a closed Admin control summary with a CAS backfill policy", () => {
    expect(mapProcessingAdminControl(adminControlRaw())).toEqual({
      schemaVersion: 1,
      configured: true,
      localEnabled: true,
      remoteEnabled: false,
      backfillPolicy: {
        schemaVersion: 1,
        revision: "9".repeat(64),
        paused: true,
        batchSize: 50,
        jobsPerHour: 500,
        bytesPerHour: 1_073_741_824,
        providerConcurrency: 2,
        capabilityConcurrency: 2,
      },
    });
  });

  it.each([
    { backfill_policy: { ...backfillPolicyRaw(), revision: "A".repeat(64) } },
    { backfill_policy: { ...backfillPolicyRaw(), jobs_per_hour: 0 } },
    { queue: { total: 0, by_state: { future: 1 }, by_priority: {}, oldest_queued_seconds: 0 } },
    { worker_path: "/forbidden" },
  ])("rejects an unsafe Admin control summary as a whole: %o", (mutation) => {
    expect(() => mapProcessingAdminControl({ ...adminControlRaw(), ...mutation })).toThrow();
  });

  it("maps one closed queued product and preserves the public job handle exactly", () => {
    expect(mapProcessingProduct(queuedProduct())).toMatchObject({
      schemaVersion: 1,
      jobId,
      state: "queued",
      representation: "thumbnail",
      pollAfterSeconds: 2,
      terminal: false,
    });
  });

  it.each([
    { state: "running" },
    { job_id: `A${jobId.slice(1)}` },
    { poll_after_seconds: 31 },
    { poll_after_seconds: 0 },
    { fallback_actions: ["native_preview", "server_path"] },
    { terminal: true },
    { capability: " image.thumbnail" },
    { raw_output: "forbidden" },
  ])("rejects an unsafe product as a whole: %o", (mutation) => {
    expect(() => mapProcessingProduct(queuedProduct(mutation))).toThrow();
  });

  it("rejects the whole processing state when one representation is malformed", () => {
    expect(() =>
      mapAssetProcessingState({
        schema_version: 1,
        representations: [queuedProduct(), queuedProduct({ representation: "future" })],
      })
    ).toThrow();
  });

  it("maps only sanitized updater capability/profile changes", () => {
    const candidates = mapProcessingUpdaterCandidates({
      schema_version: 1,
      items: [
        {
          candidate_id: "4".repeat(32),
          source_kind: "admin_registered",
          source_id: "offline-2026-07",
          version: "1.2.3",
          manifest_digest: "5".repeat(64),
          signing_key_fingerprint: "6".repeat(64),
          bundle_fingerprint: "7".repeat(64),
          state: "verified",
          verified_at: "2026-07-20T08:00:00Z",
          capability_changes: [
            {
              capability: "text.extract",
              capability_schema: "text.extract.v1",
              profiles: ["bounded_text_v1"],
            },
          ],
        },
      ],
    });
    expect(candidates[0].capabilityChanges[0]).toEqual({
      capability: "text.extract",
      capabilitySchema: "text.extract.v1",
      profiles: ["bounded_text_v1"],
    });
    expect(JSON.stringify(candidates)).not.toMatch(/path|credential|raw_output|tool_revision/);
  });
});

describe("backup asset processing API", () => {
  it("updates backfill policy through one exact CAS JSON body", async () => {
    const calls: Array<{ path: string; options: unknown }> = [];
    const requester = vi.fn(async (path: string, options: unknown) => {
      calls.push({ path, options });
      return { ...backfillPolicyRaw(), paused: false };
    });
    const api = createBackupAssetProcessingApi(requester);
    const result = await api.updateBackfillPolicy("token", {
      ...mapProcessingAdminControl(adminControlRaw()).backfillPolicy,
      paused: false,
    });

    expect(result.paused).toBe(false);
    expect(calls).toEqual([{
      path: "/admin/backup-asset-processing/backfill-policy",
      options: {
        method: "PATCH",
        token: "token",
        body: {
          schema_version: 1,
          expected_revision: "9".repeat(64),
          paused: false,
          batch_size: 50,
          jobs_per_hour: 500,
          bytes_per_hour: 1_073_741_824,
          provider_concurrency: 2,
          capability_concurrency: 2,
        },
        signal: undefined,
      },
    }]);
  });

  it("uses exact scoped routes and returns the mapped job handle", async () => {
    const calls: Array<{ path: string; options: unknown }> = [];
    const requester = vi.fn(async (path: string, options: unknown) => {
      calls.push({ path, options });
      return queuedProduct();
    });
    const api = createBackupAssetProcessingApi(requester);
    const result = await api.createPreview("token", ref, "thumbnail");

    expect(result.jobId).toBe(jobId);
    expect(calls).toEqual([
      {
        path: `/recovery-points/${ref.recoveryPointId}/entries/${ref.entryId}/preview-jobs`,
        options: {
          method: "POST",
          token: "token",
          body: { schema_version: 1, representation: "thumbnail" },
          signal: undefined,
        },
      },
    ]);
  });

  it("keeps offline administration JSON-only", async () => {
    const calls: Array<{ path: string; options: Record<string, unknown> }> = [];
    const requester = vi.fn(async (path: string, options: Record<string, unknown>) => {
      calls.push({ path, options });
      return { schema_version: 1, accepted: true };
    });
    const api = createBackupAssetProcessingApi(requester);
    await api.scanOfflineCandidates("token");
    await api.activateOfflineCandidate("token", "4".repeat(32), null);

    expect(calls[0]).toEqual({
      path: "/admin/backup-asset-processing/updater/offline-candidates/scan",
      options: { method: "POST", token: "token", signal: undefined },
    });
    expect(calls[1]).toEqual({
      path: "/admin/backup-asset-processing/updater/offline-imports",
      options: {
        method: "POST",
        token: "token",
        body: {
          schema_version: 1,
          candidate_id: "4".repeat(32),
          expected_active_fingerprint: null,
        },
        signal: undefined,
      },
    });
    expect(calls.flatMap((call) => Object.keys(call.options))).not.toContain("path");
    expect(calls.flatMap((call) => Object.keys(call.options))).not.toContain("url");
  });
});

function backfillPolicyRaw() {
  return {
    schema_version: 1,
    revision: "9".repeat(64),
    paused: true,
    batch_size: 50,
    jobs_per_hour: 500,
    bytes_per_hour: 1_073_741_824,
    provider_concurrency: 2,
    capability_concurrency: 2,
  };
}

function adminControlRaw() {
  return {
    schema_version: 1,
    configured: true,
    local_enabled: true,
    remote_enabled: false,
    backfill_policy: backfillPolicyRaw(),
    worker_counts: { active: 1, draining: 0, degraded: 0, quarantined: 0 },
    slots: { interactive_used: 0, interactive_total: 2, background_used: 0, background_total: 2 },
    queue: { total: 0, by_state: {}, by_priority: {}, oldest_queued_seconds: 0 },
    outcomes: { by_error_category: {} },
    derived: { by_state: {}, logical_bytes: 0, physical_bytes: 0, orphan_bytes: 0, quota_bytes: 1024 },
    reconciled_at: null,
  };
}
