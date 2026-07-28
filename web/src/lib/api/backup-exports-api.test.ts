import { describe, expect, it, vi } from "vitest";

import {
  createBackupExportsApi,
  mapBackupExportJob,
} from "./backup-exports-api";
import type {
  BackupExportCleanupState,
  BackupExportExecutionState,
  BackupExportItemState,
} from "@/types/domain";

const opaqueId = "1".repeat(32);
const selectionDigest = "2".repeat(64);
const FRACTIONAL_PRECISION_PAIRS = [
  [1, "1", "2"],
  [2, "01", "02"],
  [3, "001", "002"],
  [4, "0001", "0002"],
  [5, "00001", "00002"],
  [6, "000001", "000002"],
  [7, "0000001", "0000002"],
  [8, "00000001", "00000002"],
  [9, "000000001", "000000002"],
] as const;

function zuluInstant(fraction: string): string {
  return `2030-01-01T00:00:00.${fraction}Z`;
}

function plusEightInstant(fraction: string): string {
  return `2030-01-01T08:00:00.${fraction}+08:00`;
}

const zuluSecondBefore = "2029-12-31T23:59:59Z";
const zuluInstantBase = "2030-01-01T00:00:00Z";
const zuluSecondAfter = "2030-01-01T00:00:01Z";

function queuedJob() {
  const createdAt = new Date();
  return {
    schema_version: 1,
    id: opaqueId,
    selection_digest: selectionDigest,
    archive_format: "zip",
    archive_profile: "zip_deflate_v1",
    execution_state: "queued",
    cleanup_state: "none",
    item_count: 1,
    packed_count: 0,
    skipped_count: 0,
    failed_count: 0,
    logical_bytes: 0,
    provider_bytes: 0,
    artifact_bytes: 0,
    created_at: createdAt.toISOString(),
    absolute_deadline: new Date(createdAt.getTime() + 60 * 60 * 1000).toISOString(),
    items: [
      {
        id: "3".repeat(32),
        ordinal: 0,
        state: "pending",
        logical_bytes: 0,
        provider_bytes: 0,
      },
    ],
    poll_after_seconds: 2,
    can_cancel: true,
    can_download: false,
  };
}

function pendingItem(ordinal: number) {
  return {
    id: (ordinal + 3).toString(16).repeat(32),
    ordinal,
    state: "pending",
    logical_bytes: 0,
    provider_bytes: 0,
  };
}

function jobForExecutionState(executionState: BackupExportExecutionState) {
  const active = executionState === "queued" || executionState === "running" || executionState === "retry_wait" ||
    executionState === "sealing" || executionState === "cancel_requested" || executionState === "expiring";
  const cancelable = executionState === "queued" || executionState === "running" || executionState === "retry_wait" ||
    executionState === "sealing" || executionState === "ready" || executionState === "cancel_requested";
  const raw: Record<string, unknown> = {
    ...queuedJob(),
    execution_state: executionState,
    poll_after_seconds: active ? 2 : 0,
    can_cancel: cancelable,
  };
  if (executionState === "running" || executionState === "sealing") {
    raw.attempt = {
      attempt_number: 1,
      state: executionState === "running" ? "active" : "sealing",
      item_count: executionState === "running" ? 0 : 1,
      logical_bytes: executionState === "running" ? 0 : 5,
      provider_bytes: executionState === "running" ? 0 : 3,
      lease_expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
    };
  }
  if (executionState === "sealing" || executionState === "ready" || executionState === "expiring" || executionState === "expired") {
    raw.result_kind = "complete";
    raw.packed_count = 1;
    raw.logical_bytes = 5;
    raw.provider_bytes = 3;
    raw.artifact_bytes = executionState === "sealing" ? 0 : 128;
    raw.items = [{ id: "3".repeat(32), ordinal: 0, state: "packed", logical_bytes: 5, provider_bytes: 3 }];
  }
  if (executionState === "ready" || executionState === "expiring" || executionState === "expired") {
    const readyAt = Date.parse(String(raw.created_at)) + 2_000;
    raw.ready_at = new Date(readyAt).toISOString();
    raw.expires_at = new Date(readyAt + (executionState === "expired" ? 1_000 : 30 * 60 * 1000)).toISOString();
    raw.can_download = executionState === "ready";
  }
  if (executionState === "ready") {
    raw.attempt = {
      attempt_number: 1,
      state: "sealed",
      item_count: 1,
      logical_bytes: 5,
      provider_bytes: 3,
      lease_expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
    };
  }
  if (executionState === "failed" || executionState === "source_expired") {
    raw.error_category = executionState === "source_expired" ? "source_expired" : "internal_failure";
  }
  return raw;
}

function jobForLifecycleProduct(
  executionState: BackupExportExecutionState,
  cleanupState: BackupExportCleanupState,
) {
  const raw = jobForExecutionState(executionState);
  raw.cleanup_state = cleanupState;
  if (executionState !== "ready" || cleanupState !== "none") raw.can_download = false;
  return raw;
}

function rawExportTicket(
  contentUrl = `/api/v1/asset-content/${opaqueId}`,
  contentType = "application/zip",
) {
  return {
    schema_version: 1,
    content_url: contentUrl,
    content_type: contentType,
    content_length: 128,
    etag: '"export-etag"',
    range: "single",
    expires_at: new Date(Date.now() + 60_000).toISOString(),
    idle_expires_at: new Date(Date.now() + 30_000).toISOString(),
  };
}

describe("backup export API boundary", () => {
  it("maps one closed queued job atomically", () => {
    const raw = queuedJob();
    expect(mapBackupExportJob(raw)).toEqual({
      schemaVersion: 1,
      id: opaqueId,
      selectionDigest,
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
      executionState: "queued",
      resultKind: null,
      cleanupState: "none",
      itemCount: 1,
      packedCount: 0,
      skippedCount: 0,
      failedCount: 0,
      logicalBytes: 0,
      providerBytes: 0,
      artifactBytes: 0,
      errorCategory: null,
      createdAt: raw.created_at,
      absoluteDeadline: raw.absolute_deadline,
      readyAt: null,
      expiresAt: null,
      attempt: null,
      items: [
        {
          id: "3".repeat(32),
          ordinal: 0,
          state: "pending",
          logicalBytes: 0,
          providerBytes: 0,
          errorCategory: null,
        },
      ],
      nextCursor: null,
      pollAfterSeconds: 2,
      canCancel: true,
      canDownload: false,
    });
  });

  it("maps the job creation time before its immutable deadline", () => {
    const raw = queuedJob();
    const createdAt = new Date(Date.parse(raw.absolute_deadline) - 60 * 60 * 1000).toISOString();

    expect(mapBackupExportJob({ ...raw, created_at: createdAt })).toMatchObject({ createdAt });
  });

  it("rejects a human-form timestamp that Date.parse accepts", () => {
    const raw = queuedJob();

    expect(() => mapBackupExportJob({
      ...raw,
      created_at: raw.created_at.replace("T", " "),
    })).toThrow("invalid backup export response");
  });

  it("rejects an impossible RFC3339 calendar date rather than normalizing it", () => {
    const raw = queuedJob();

    expect(() => mapBackupExportJob({
      ...raw,
      created_at: "2026-02-30T00:00:00Z",
    })).toThrow("invalid backup export response");
  });

  it.each([
    "2027-01-01T24:00:00Z",
    "2027-01-01T00:00:00+24:00",
    "2027-01-01T00:00:00+23:60",
    "2027-01-01T00:00:00-24:00",
    "2027-01-01T00:00:00-23:60",
  ])("rejects an out-of-range RFC3339 clock or offset: %s", (createdAt) => {
    expect(() => mapBackupExportJob({ ...queuedJob(), created_at: createdAt }))
      .toThrow("invalid backup export response");
  });

  it("rejects RFC3339's unknown local offset -00:00", () => {
    const raw = queuedJob();
    raw.created_at = "2027-01-01T00:00:00-00:00";
    raw.absolute_deadline = "2027-01-01T00:00:00Z";

    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it.each([
    "9999-12-31T23:59:59-00:01",
    "0000-01-01T00:00:00+00:01",
  ])("rejects an RFC3339 instant whose UTC normalization escapes the four-digit year range: %s", (instant) => {
    const raw = queuedJob();
    raw.created_at = instant;
    raw.absolute_deadline = instant;

    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it("rejects a fractional timestamp wider than RFC3339 nanoseconds", () => {
    expect(() => mapBackupExportJob({
      ...queuedJob(),
      created_at: "2027-01-01T00:00:00.1000000000+00:30",
    })).toThrow("invalid backup export response");
  });

  it.each(["ready_at", "expires_at"])("rejects a present empty optional timestamp: %s", (field) => {
    expect(() => mapBackupExportJob({ ...queuedJob(), [field]: "" }))
      .toThrow("invalid backup export response");
  });

  it("rejects a job created after its immutable deadline", () => {
    const raw = queuedJob();

    expect(() => mapBackupExportJob({
      ...raw,
      created_at: new Date(Date.parse(raw.absolute_deadline) + 1).toISOString(),
    })).toThrow("invalid backup export response");
  });

  it("rejects a ready artifact timestamp one millisecond before creation", () => {
    const raw = jobForExecutionState("ready");
    const createdAt = Date.parse(String(raw.created_at));
    raw.ready_at = new Date(createdAt - 1).toISOString();
    raw.expires_at = new Date(createdAt + 1_000).toISOString();

    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it("rejects a ready artifact timestamp before creation below millisecond precision", () => {
    const raw = jobForExecutionState("ready");
    raw.created_at = "2027-01-01T00:00:00.0009Z";
    raw.ready_at = "2027-01-01T00:00:00.0001Z";
    raw.expires_at = "2027-01-01T00:00:01Z";
    raw.absolute_deadline = "2027-01-01T00:01:00Z";

    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it("accepts equal ready and creation instants at nanosecond precision across offsets", () => {
    const raw = jobForExecutionState("ready");
    raw.created_at = "2027-01-01T08:00:00.000123456+08:00";
    raw.ready_at = "2027-01-01T00:00:00.000123456Z";
    raw.expires_at = "2027-01-01T00:00:01Z";
    raw.absolute_deadline = "2027-01-01T00:01:00Z";

    expect(mapBackupExportJob(raw)).toMatchObject({
      createdAt: "2027-01-01T00:00:00.000123456Z",
      readyAt: "2027-01-01T00:00:00.000123456Z",
    });
  });

  it("accepts equal mixed-width creation and deadline instants across offsets", () => {
    const raw = queuedJob();
    raw.created_at = "2030-01-01T00:00:00.1Z";
    raw.absolute_deadline = "2030-01-01T08:00:00.100000000+08:00";

    expect(mapBackupExportJob(raw)).toMatchObject({
      createdAt: "2030-01-01T00:00:00.100Z",
      absoluteDeadline: "2030-01-01T00:00:00.100Z",
    });
  });

  it("rejects a creation instant one nanosecond after a mixed-width deadline across offsets", () => {
    const raw = queuedJob();
    raw.created_at = "2030-01-01T00:00:00.100000001Z";
    raw.absolute_deadline = "2030-01-01T08:00:00.1+08:00";

    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it("accepts equal creation and deadline instants through a negative half-hour UTC rollover", () => {
    const raw = queuedJob();
    raw.created_at = "2029-12-31T23:30:00.1-00:30";
    raw.absolute_deadline = "2030-01-01T00:00:00.100000000Z";

    expect(mapBackupExportJob(raw)).toMatchObject({
      createdAt: "2030-01-01T00:00:00.100Z",
      absoluteDeadline: "2030-01-01T00:00:00.100Z",
    });
  });

  it("rejects a negative half-hour rollover creation instant one nanosecond after its deadline", () => {
    const raw = queuedJob();
    raw.created_at = "2029-12-31T23:30:00.100000001-00:30";
    raw.absolute_deadline = "2030-01-01T00:00:00.1Z";

    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "rejects a creation time after its deadline at %i-digit fractional precision across offsets",
    (_digits, fraction) => {
      const raw = queuedJob();
      raw.created_at = plusEightInstant(fraction);
      raw.absolute_deadline = zuluInstantBase;

      expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
    },
  );

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "accepts equal creation and deadline instants at %i-digit fractional precision across offsets",
    (_digits, fraction) => {
      const raw = queuedJob();
      raw.created_at = plusEightInstant(fraction);
      raw.absolute_deadline = zuluInstant(fraction);

      expect(() => mapBackupExportJob(raw)).not.toThrow();
    },
  );

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "accepts equal creation and ready instants at %i-digit fractional precision across offsets",
    (_digits, fraction) => {
      const raw = jobForExecutionState("ready");
      raw.created_at = plusEightInstant(fraction);
      raw.ready_at = zuluInstant(fraction);
      raw.expires_at = zuluSecondAfter;
      raw.absolute_deadline = zuluSecondAfter;

      expect(() => mapBackupExportJob(raw)).not.toThrow();
    },
  );

  it("accepts a ready artifact that outlives its execution deadline at nanosecond precision across offsets", () => {
    const raw = jobForExecutionState("ready");
    raw.created_at = "2030-01-01T00:00:00.000000001Z";
    raw.absolute_deadline = "2030-01-01T00:00:00.000000002Z";
    raw.ready_at = "2030-01-01T08:00:00.000000002+08:00";
    raw.expires_at = "2030-01-01T00:00:00.000000003Z";

    expect(() => mapBackupExportJob(raw)).not.toThrow();
  });

  it("rejects an artifact expiry without a ready timestamp", () => {
    const raw = queuedJob();

    expect(() => mapBackupExportJob({ ...raw, expires_at: raw.created_at }))
      .toThrow("invalid backup export response");
  });

  it("rejects a ready timestamp without an artifact expiry", () => {
    const raw = jobForExecutionState("ready");
    delete raw.expires_at;

    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "accepts a strictly later artifact expiry at %i-digit fractional precision across offsets",
    (_digits, fraction, nextFraction) => {
      const raw = jobForExecutionState("ready");
      raw.created_at = zuluSecondBefore;
      raw.ready_at = plusEightInstant(fraction);
      raw.expires_at = zuluInstant(nextFraction);
      raw.absolute_deadline = zuluSecondAfter;

      expect(() => mapBackupExportJob(raw)).not.toThrow();
    },
  );

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "rejects an artifact expiry equal to ready time at %i-digit fractional precision across offsets",
    (_digits, fraction) => {
      const raw = jobForExecutionState("ready");
      raw.created_at = zuluSecondBefore;
      raw.ready_at = plusEightInstant(fraction);
      raw.expires_at = zuluInstant(fraction);
      raw.absolute_deadline = zuluSecondAfter;

      expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
    },
  );

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "rejects an attempt lease after its deadline at %i-digit fractional precision across offsets",
    (_digits, fraction) => {
      const raw = jobForExecutionState("running");
      raw.created_at = zuluInstantBase;
      raw.absolute_deadline = zuluInstantBase;
      raw.attempt = {
        attempt_number: 1,
        state: "active",
        item_count: 0,
        logical_bytes: 0,
        provider_bytes: 0,
        lease_expires_at: plusEightInstant(fraction),
      };

      expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
    },
  );

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "accepts an attempt lease equal to its deadline at %i-digit fractional precision across offsets",
    (_digits, fraction) => {
      const raw = jobForExecutionState("running");
      raw.created_at = zuluInstantBase;
      raw.absolute_deadline = zuluInstant(fraction);
      raw.attempt = {
        attempt_number: 1,
        state: "active",
        item_count: 0,
        logical_bytes: 0,
        provider_bytes: 0,
        lease_expires_at: plusEightInstant(fraction),
      };

      expect(() => mapBackupExportJob(raw)).not.toThrow();
    },
  );

  it("accepts a normalized internal failure for the job and item", () => {
    const raw = {
      ...queuedJob(),
      execution_state: "failed",
      failed_count: 1,
      error_category: "internal_failure",
      items: [{
        id: "3".repeat(32),
        ordinal: 0,
        state: "failed",
        logical_bytes: 0,
        provider_bytes: 0,
        error_category: "internal_failure",
      }],
      poll_after_seconds: 0,
      can_cancel: false,
    };

    expect(mapBackupExportJob(raw)).toMatchObject({
      executionState: "failed",
      errorCategory: "internal_failure",
      items: [{ errorCategory: "internal_failure" }],
      pollAfterSeconds: 0,
      canCancel: false,
      canDownload: false,
    });
  });

  it.each(["archive_failed", "heartbeat_lost"])("rejects raw internal category %s", (category) => {
    expect(() => mapBackupExportJob({
      ...queuedJob(),
      execution_state: "failed",
      failed_count: 1,
      error_category: category,
      items: [{
        id: "3".repeat(32),
        ordinal: 0,
        state: "failed",
        logical_bytes: 0,
        provider_bytes: 0,
        error_category: category,
      }],
      poll_after_seconds: 0,
      can_cancel: false,
    })).toThrow("invalid backup export response");
  });

  it("rejects an attempt lease beyond the immutable job deadline", () => {
    const raw = queuedJob();
    expect(() => mapBackupExportJob({
      ...raw,
      execution_state: "running",
      attempt: {
        attempt_number: 1,
        state: "active",
        item_count: 0,
        logical_bytes: 0,
        provider_bytes: 0,
        lease_expires_at: new Date(Date.parse(raw.absolute_deadline) + 1).toISOString(),
      },
    })).toThrow("invalid backup export response");
  });

  it("rejects a sealed partial result whose counters do not cover the frozen selection", () => {
    const raw = queuedJob();
    const readyAt = new Date(Date.now() + 1_000).toISOString();
    const expiresAt = new Date(Date.now() + 30 * 60 * 1000).toISOString();
    expect(() => mapBackupExportJob({
      ...raw,
      execution_state: "ready",
      result_kind: "partial",
      item_count: 3,
      packed_count: 1,
      skipped_count: 1,
      artifact_bytes: 128,
      ready_at: readyAt,
      expires_at: expiresAt,
      poll_after_seconds: 0,
      items: [{
        id: "3".repeat(32),
        ordinal: 0,
        state: "packed",
        logical_bytes: 1024,
        provider_bytes: 1024,
      }],
      can_download: true,
    })).toThrow("invalid backup export response");
  });

  it("rejects a ready result without a sealed artifact", () => {
    expect(() => mapBackupExportJob({
      ...jobForExecutionState("ready"),
      artifact_bytes: 0,
    })).toThrow("invalid backup export response");
  });

  const contradictoryJobs: ReadonlyArray<readonly [string, () => Record<string, unknown>]> = [
    ["queued artifact bytes", () => ({ ...queuedJob(), artifact_bytes: 128 })],
    ["queued result", () => ({
      ...queuedJob(),
      result_kind: "complete",
      packed_count: 1,
      logical_bytes: 5,
      provider_bytes: 3,
      items: [{ id: "3".repeat(32), ordinal: 0, state: "packed", logical_bytes: 5, provider_bytes: 3 }],
    })],
    ["running ready timestamps", () => {
      const raw = jobForExecutionState("running");
      const readyAt = new Date(Date.parse(String(raw.created_at)) + 1_000).toISOString();
      return { ...raw, ready_at: readyAt, expires_at: new Date(Date.parse(readyAt) + 60_000).toISOString() };
    }],
    ...(["failed", "source_expired", "canceled"] as const).map((executionState) => [
      `${executionState} artifact product`,
      () => {
        const raw = jobForExecutionState("ready");
        delete raw.attempt;
        return {
          ...raw,
          execution_state: executionState,
          error_category: executionState === "source_expired" ? "source_expired" : executionState === "failed" ? "internal_failure" : undefined,
          can_cancel: false,
          can_download: false,
        };
      },
    ] as const),
  ];

  it.each(contradictoryJobs)("rejects contradictory %s fields", (_description, raw) => {
    expect(() => mapBackupExportJob(raw())).toThrow("invalid backup export response");
  });

  it("preserves server-authorized download access across browser clock skew", () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date("2030-01-01T00:00:00.000Z"));
      const raw = jobForExecutionState("ready");
      raw.created_at = new Date(Date.now() - 120_000).toISOString();
      raw.ready_at = new Date(Date.now() - 60_000).toISOString();
      raw.expires_at = new Date(Date.now() - 1).toISOString();
      raw.can_download = true;

      expect(mapBackupExportJob(raw).canDownload).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it.each([
    ["a non-ready job", { ...queuedJob(), can_download: true }],
    ["a job undergoing cleanup", { ...jobForExecutionState("ready"), cleanup_state: "purging", can_download: true }],
  ])("rejects server download access for %s", (_name, raw) => {
    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it("rejects an empty frozen export job", () => {
    expect(() => mapBackupExportJob({
      ...queuedJob(),
      item_count: 0,
      items: [],
    })).toThrow("invalid backup export response");
  });

  it("rejects attempt progress that contradicts the authoritative job projection", () => {
    const raw = queuedJob();
    expect(() => mapBackupExportJob({
      ...raw,
      execution_state: "running",
      attempt: {
        attempt_number: 1,
        state: "active",
        item_count: 1,
        logical_bytes: 0,
        provider_bytes: 0,
        lease_expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
      },
    })).toThrow("invalid backup export response");
  });

  it("rejects an attempt state that contradicts job execution", () => {
    expect(() => mapBackupExportJob({
      ...queuedJob(),
      execution_state: "running",
      attempt: {
        attempt_number: 1,
        state: "sealed",
        item_count: 0,
        logical_bytes: 0,
        provider_bytes: 0,
        lease_expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
      },
    })).toThrow("invalid backup export response");
  });

  it.each([
    "queued", "running", "retry_wait", "sealing", "ready", "cancel_requested",
    "failed", "source_expired", "canceled", "expiring", "expired",
  ] satisfies BackupExportExecutionState[])("maps the closed %s execution state", (executionState) => {
    const cleanupState = executionState === "canceled" || executionState === "expired" ? "purged" : "none";
    expect(mapBackupExportJob(jobForLifecycleProduct(executionState, cleanupState)).executionState).toBe(executionState);
  });

  it.each([
    "none", "revoking", "purging", "purged", "purge_failed",
  ] satisfies BackupExportCleanupState[])("maps the closed %s cleanup state", (cleanupState) => {
    expect(mapBackupExportJob({
      ...jobForExecutionState("failed"),
      cleanup_state: cleanupState,
    }).cleanupState).toBe(cleanupState);
  });

  const allowedLifecycleProducts: ReadonlyArray<readonly [
    BackupExportExecutionState,
    readonly BackupExportCleanupState[],
  ]> = [
    ["queued", ["none"]],
    ["running", ["none"]],
    ["retry_wait", ["none"]],
    ["sealing", ["none"]],
    ["ready", ["none"]],
    ["cancel_requested", ["none", "revoking", "purging", "purged", "purge_failed"]],
    ["failed", ["none", "revoking", "purging", "purged", "purge_failed"]],
    ["source_expired", ["none", "revoking", "purging", "purged", "purge_failed"]],
    ["expiring", ["none", "revoking", "purging", "purged", "purge_failed"]],
    ["canceled", ["purged", "purge_failed"]],
    ["expired", ["purged", "purge_failed"]],
  ];
  const allCleanupStates: readonly BackupExportCleanupState[] = [
    "none", "revoking", "purging", "purged", "purge_failed",
  ];

  it.each(allowedLifecycleProducts.flatMap(([executionState, cleanupStates]) =>
    cleanupStates.map((cleanupState) => [executionState, cleanupState] as const),
  ))("maps the valid %s x %s lifecycle product", (executionState, cleanupState) => {
    expect(mapBackupExportJob(jobForLifecycleProduct(executionState, cleanupState))).toMatchObject({
      executionState,
      cleanupState,
    });
  });

  it.each(allowedLifecycleProducts.flatMap(([executionState, cleanupStates]) =>
    allCleanupStates
      .filter((cleanupState) => !cleanupStates.includes(cleanupState))
      .map((cleanupState) => [executionState, cleanupState] as const),
  ))("rejects the impossible %s x %s lifecycle product", (executionState, cleanupState) => {
    expect(() => mapBackupExportJob(jobForLifecycleProduct(executionState, cleanupState)))
      .toThrow("invalid backup export response");
  });

  it.each([
    ["pending", 0, 0, null],
    ["read", 0, 0, null],
    ["packed", 5, 3, null],
    ["skipped", 0, 0, "link_metadata_unavailable"],
    ["failed", 0, 0, "source_changed"],
  ] satisfies Array<[BackupExportItemState, number, number, string | null]>)(
    "maps the closed %s item state",
    (itemState, logicalBytes, providerBytes, errorCategory) => {
      const terminal = itemState === "packed" || itemState === "skipped" || itemState === "failed";
      const raw = jobForExecutionState("running");
      raw.packed_count = itemState === "packed" ? 1 : 0;
      raw.skipped_count = itemState === "skipped" ? 1 : 0;
      raw.failed_count = itemState === "failed" ? 1 : 0;
      raw.logical_bytes = logicalBytes;
      raw.provider_bytes = providerBytes;
      raw.items = [{
        id: "3".repeat(32), ordinal: 0, state: itemState,
        logical_bytes: logicalBytes, provider_bytes: providerBytes,
        ...(errorCategory === null ? {} : { error_category: errorCategory }),
      }];
      raw.attempt = {
        attempt_number: 1,
        state: "active",
        item_count: terminal ? 1 : 0,
        logical_bytes: logicalBytes,
        provider_bytes: providerBytes,
        lease_expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
      };
      expect(mapBackupExportJob(raw).items[0]?.state).toBe(itemState);
    },
  );

  it.each(["failed", "canceled", "superseded"])("rejects historical %s attempt state as current", (attemptState) => {
    const raw = jobForExecutionState("running");
    raw.attempt = { ...(raw.attempt as Record<string, unknown>), state: attemptState };
    expect(() => mapBackupExportJob(raw)).toThrow("invalid backup export response");
  });

  it.each([
    ["unknown execution state", { execution_state: "future" }],
    ["unknown cleanup state", { cleanup_state: "deleted" }],
    ["unknown item state", { items: [{ id: "3".repeat(32), ordinal: 0, state: "future", logical_bytes: 0, provider_bytes: 0 }] }],
    ["cancel capability contradicts execution state", { can_cancel: false }],
    ["duplicate item ordinal", { items: [
      { id: "3".repeat(32), ordinal: 0, state: "pending", logical_bytes: 0, provider_bytes: 0 },
      { id: "4".repeat(32), ordinal: 0, state: "pending", logical_bytes: 0, provider_bytes: 0 },
    ], item_count: 2 }],
    ["unsafe count", { logical_bytes: Number.MAX_SAFE_INTEGER + 1 }],
    ["query-bearing artifact is not accepted", { next_cursor: "cursor?secret" }],
  ])("rejects the whole product for %s", (_name, mutation) => {
    expect(() => mapBackupExportJob({ ...queuedJob(), ...mutation })).toThrow();
  });

  it("maps explicit create and serializes only the frozen refs", async () => {
    const calls: Array<{ path: string; options: unknown }> = [];
    const requester = async (path: string, options: unknown) => {
      calls.push({ path, options });
      const raw = queuedJob();
      return { job: raw, replay: false };
    };
    const refs = [{ recoveryPointId: "a".repeat(32), entryId: "b".repeat(64) }];
    const api = createBackupExportsApi(requester);
    await api.create("token", {
      selection: { schemaVersion: 1, kind: "explicit", refs },
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
      idempotencyKey: "export-idempotency-0001",
    }, "fresh-export-create-proof");
    expect(calls).toEqual([{
      path: "/asset-exports",
      options: {
        method: "POST",
        token: "token",
        stepUpProof: "fresh-export-create-proof",
        idempotencyKey: "export-idempotency-0001",
        signal: undefined,
        body: {
          schema_version: 1,
          selection: {
            schema_version: 1,
            kind: "explicit",
            refs: [{ recovery_point_id: "a".repeat(32), entry_id: "b".repeat(64) }],
          },
          archive_format: "zip",
          archive_profile: "zip_deflate_v1",
        },
      },
    }]);
  });

  it("forwards a fresh export-create proof without persisting it", async () => {
    const calls: Array<{ path: string; options: Record<string, unknown> }> = [];
    const requester = async (path: string, options: Record<string, unknown>) => {
      calls.push({ path, options });
      return { job: queuedJob(), replay: false };
    };
    const api = createBackupExportsApi(requester);
    await api.create("token", {
      selection: { schemaVersion: 1, kind: "explicit", refs: [{ recoveryPointId: "a".repeat(32), entryId: "b".repeat(64) }] },
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
      idempotencyKey: "export-idempotency-proof",
    }, "fresh-export-create-proof");

    expect(calls[0]?.options.stepUpProof).toBe("fresh-export-create-proof");
  });

  it("removes an inaccessible export job only from the 401 login return target", async () => {
    const originalSearch = `?repository=${"a".repeat(32)}&exportJobId=${opaqueId}&layout=table`;
    let loginRedirect = "";
    const location = {
      pathname: "/app/backups/data",
      search: originalSearch,
      hash: "#export-status",
      hostname: "console.example.test",
      get href() {
        return loginRedirect;
      },
      set href(value: string) {
        loginRedirect = value;
      },
    };
    const fetchMock = vi.fn().mockResolvedValue({
      status: 401,
      ok: false,
      headers: { get: vi.fn().mockReturnValue(null) },
      text: vi.fn().mockResolvedValue(JSON.stringify({ code: 401, message: "session expired" })),
    } as unknown as Response);
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("window", { location });

    try {
      await expect(createBackupExportsApi().status("token", opaqueId)).rejects.toMatchObject({ status: 401 });

      expect(fetchMock).toHaveBeenCalledWith(
        `/api/v1/asset-exports/${opaqueId}?items_limit=100`,
        expect.objectContaining({ method: "GET" }),
      );
      expect(loginRedirect).toBe(
        `/login?redirect=${encodeURIComponent(`/app/backups/data?repository=${"a".repeat(32)}&layout=table#export-status`)}`,
      );
      expect(location.search).toBe(originalSearch);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("preserves unrelated query bytes when removing every export job from the 401 return target", async () => {
    const originalSearch = "?encoded=%2f%20&flag&duplicate=first&&exportJobId=raw%2f%20&duplicate=second&%65xportJobId=encoded&empty=&tail=%2F%20";
    const preservedSearch = "?encoded=%2f%20&flag&duplicate=first&&duplicate=second&empty=&tail=%2F%20";
    let loginRedirect = "";
    const location = {
      pathname: "/app/backups/data",
      search: originalSearch,
      hash: "#export-status%2f",
      hostname: "console.example.test",
      get href() {
        return loginRedirect;
      },
      set href(value: string) {
        loginRedirect = value;
      },
    };
    const fetchMock = vi.fn().mockResolvedValue({
      status: 401,
      ok: false,
      headers: { get: vi.fn().mockReturnValue(null) },
      text: vi.fn().mockResolvedValue(JSON.stringify({ code: 401, message: "session expired" })),
    } as unknown as Response);
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("window", { location });

    try {
      await expect(createBackupExportsApi().status("token", opaqueId)).rejects.toMatchObject({ status: 401 });

      expect(loginRedirect).toBe(
        `/login?redirect=${encodeURIComponent(`/app/backups/data${preservedSearch}#export-status%2f`)}`,
      );
      expect(location.search).toBe(originalSearch);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("keeps saved-search create typed for API-only callers", async () => {
    const requester = async (_path: string, _options: unknown) => ({ job: queuedJob(), replay: true });
    await createBackupExportsApi(requester).create("token", {
      selection: {
        schemaVersion: 1,
        kind: "saved_search",
        savedSearchId: "c".repeat(32),
        savedSearchVersion: 7,
      },
      archiveFormat: "tar",
      archiveProfile: "tar_gzip_v1",
      idempotencyKey: "export-idempotency-0002",
    }, "fresh-export-create-proof");
  });

  it.each([
    ["an unsupported selection schema", {
      schemaVersion: 2,
      kind: "explicit",
      refs: [{ recoveryPointId: "a".repeat(32), entryId: "b".repeat(64) }],
    } as never],
    ["an unknown selection discriminator", {
      schemaVersion: 1,
      kind: "directory",
      savedSearchId: "c".repeat(32),
      savedSearchVersion: 7,
    } as never],
  ])("rejects %s before transport", async (_name, selection) => {
    const requester = vi.fn().mockResolvedValue({ job: queuedJob(), replay: false });

    await expect(createBackupExportsApi(requester).create("token", {
      selection,
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
      idempotencyKey: "export-idempotency-invalid",
    }, "fresh-export-create-proof")).rejects.toThrow("invalid backup export request");
    expect(requester).not.toHaveBeenCalled();
  });

  it("rejects a status response bound to a different export job", async () => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...queuedJob(),
      id: "4".repeat(32),
    }));

    await expect(api.status("token", opaqueId)).rejects.toThrow("invalid backup export response");
  });

  it("rejects a status page larger than its requested item limit", async () => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...queuedJob(),
      item_count: 2,
      items: [pendingItem(0), pendingItem(1)],
    }));

    await expect(api.status("token", opaqueId, { limit: 1 })).rejects.toThrow("invalid backup export response");
  });

  it.each([
    ["an empty page", 1, []],
    ["a short page", 2, [pendingItem(0)]],
  ])("rejects %s that advertises a continuation cursor", async (_name, itemCount, items) => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...queuedJob(),
      item_count: itemCount,
      items,
      next_cursor: "next_page",
    }));

    await expect(api.status("token", opaqueId, { limit: 2 })).rejects.toThrow("invalid backup export response");
  });

  it.each([
    ["a gap", 3, [pendingItem(0), pendingItem(2)]],
    ["descending ordinals", 2, [pendingItem(1), pendingItem(0)]],
  ])("rejects a status page with %s", async (_name, itemCount, items) => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...queuedJob(),
      item_count: itemCount,
      items,
    }));

    await expect(api.status("token", opaqueId, { limit: 2 })).rejects.toThrow("invalid backup export response");
  });

  it.each([
    ["a first page", undefined, [pendingItem(0), pendingItem(1)], "next_page"],
    ["a later terminal page", "opaque_cursor", [pendingItem(2), pendingItem(3)], undefined],
  ])("accepts %s while keeping the cursor opaque", async (_name, cursor, items, nextCursor) => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...queuedJob(),
      item_count: 4,
      items,
      ...(nextCursor === undefined ? {} : { next_cursor: nextCursor }),
    }));

    const job = await api.status("token", opaqueId, { cursor, limit: 2 });

    expect(job.items.map((item) => item.ordinal)).toEqual(items.map((item) => item.ordinal));
    expect(job.nextCursor).toBe(nextCursor ?? null);
  });

  it("rejects a cancel response bound to a different export job", async () => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...queuedJob(),
      id: "4".repeat(32),
    }));

    await expect(api.cancel("token", opaqueId)).rejects.toThrow("invalid backup export response");
  });

  it("rejects duplicate explicit composite refs before issuing a request", async () => {
    const requester = vi.fn().mockResolvedValue({ job: queuedJob(), replay: false });
    const duplicate = { recoveryPointId: "a".repeat(32), entryId: "b".repeat(64) };
    const api = createBackupExportsApi(requester);

    await expect(api.create("token", {
      selection: { schemaVersion: 1, kind: "explicit", refs: [duplicate, { ...duplicate }] },
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
      idempotencyKey: "export-idempotency-duplicate",
    }, "fresh-export-create-proof")).rejects.toThrow("invalid backup export request");
    expect(requester).not.toHaveBeenCalled();
  });

  it("rejects a malformed download proof before issuing a request", async () => {
    const requester = vi.fn();

    await expect(createBackupExportsApi(requester).issueDownloadTicket(
      "token",
      opaqueId,
      "fresh\nexport-download-proof",
    )).rejects.toThrow("invalid backup export request");
    expect(requester).not.toHaveBeenCalled();
  });

  it("maps a canonical single-range export ticket", async () => {
    const ticket = await createBackupExportsApi(vi.fn().mockResolvedValue(rawExportTicket()))
      .issueDownloadTicket("token", opaqueId, "fresh-export-download-proof");

    expect(ticket).toMatchObject({
      contentUrl: `/api/v1/asset-content/${opaqueId}`,
      range: "single",
    });
  });

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "accepts a ticket valid after the exact browser clock at %i-digit fractional precision across offsets",
    async (_digits, fraction) => {
      vi.useFakeTimers();
      try {
        vi.setSystemTime(new Date(zuluInstantBase));
        const ticket = rawExportTicket();
        ticket.expires_at = plusEightInstant(fraction);
        ticket.idle_expires_at = zuluInstant(fraction);

        await expect(createBackupExportsApi(vi.fn().mockResolvedValue(ticket))
          .issueDownloadTicket("token", opaqueId, "fresh-export-download-proof"))
          .resolves.toMatchObject({ schemaVersion: 1 });
      } finally {
        vi.useRealTimers();
      }
    },
  );

  it.each(FRACTIONAL_PRECISION_PAIRS)(
    "rejects a ticket idle expiry after its absolute expiry at %i-digit fractional precision across offsets",
    async (_digits, fraction, nextFraction) => {
      vi.useFakeTimers();
      try {
        vi.setSystemTime(new Date(zuluSecondBefore));
        const ticket = rawExportTicket();
        ticket.expires_at = plusEightInstant(fraction);
        ticket.idle_expires_at = zuluInstant(nextFraction);

        await expect(createBackupExportsApi(vi.fn().mockResolvedValue(ticket))
          .issueDownloadTicket("token", opaqueId, "fresh-export-download-proof"))
          .rejects.toThrow("invalid backup export ticket");
      } finally {
        vi.useRealTimers();
      }
    },
  );

  it("rejects a ticket that expires at the exact browser clock", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date(zuluInstantBase));
      const ticket = rawExportTicket();
      ticket.expires_at = zuluInstantBase;
      ticket.idle_expires_at = zuluInstantBase;

      await expect(createBackupExportsApi(vi.fn().mockResolvedValue(ticket))
        .issueDownloadTicket("token", opaqueId, "fresh-export-download-proof"))
        .rejects.toThrow("invalid backup export ticket");
    } finally {
      vi.useRealTimers();
    }
  });

  it("rejects a ticket whose idle expiry is at the exact browser clock", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date(zuluInstantBase));
      const ticket = rawExportTicket();
      ticket.expires_at = zuluSecondAfter;
      ticket.idle_expires_at = zuluInstantBase;

      await expect(createBackupExportsApi(vi.fn().mockResolvedValue(ticket))
        .issueDownloadTicket("token", opaqueId, "fresh-export-download-proof"))
        .rejects.toThrow("invalid backup export ticket");
    } finally {
      vi.useRealTimers();
    }
  });

  it("rejects an expired export ticket", async () => {
    const now = Date.now();
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...rawExportTicket(),
      expires_at: new Date(now - 1_000).toISOString(),
      idle_expires_at: new Date(now - 2_000).toISOString(),
    }));

    await expect(api.issueDownloadTicket(
      "token",
      opaqueId,
      "fresh-export-download-proof",
    )).rejects.toThrow("invalid backup export ticket");
  });

  it("rejects an impossible timestamp in an export ticket", async () => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...rawExportTicket(),
      expires_at: "2999-02-30T00:00:00Z",
    }));

    await expect(api.issueDownloadTicket(
      "token",
      opaqueId,
      "fresh-export-download-proof",
    )).rejects.toThrow();
  });

  it("rejects a non-string export ETag even when coercion is syntactically valid", async () => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue({
      ...rawExportTicket(),
      etag: { toString: () => '"export-etag"' },
    }));

    await expect(api.issueDownloadTicket(
      "token",
      opaqueId,
      "fresh-export-download-proof",
    )).rejects.toThrow("invalid backup export ticket");
  });

  it.each([
    "application/zip",
    "application/x-tar",
    "application/gzip",
  ])("accepts the closed export ticket MIME %s", async (contentType) => {
    const ticket = await createBackupExportsApi(vi.fn().mockResolvedValue(
      rawExportTicket(`/api/v1/asset-content/${opaqueId}`, contentType),
    )).issueDownloadTicket("token", opaqueId, "fresh-export-download-proof");

    expect(ticket.contentType).toBe(contentType);
  });

  it.each([
    "",
    "application/octet-stream",
    "application/x-gzip",
    "text/plain",
    "application/zip; charset=binary",
  ])("rejects an export ticket MIME outside the closed set: %s", async (contentType) => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue(
      rawExportTicket(`/api/v1/asset-content/${opaqueId}`, contentType),
    ));

    await expect(api.issueDownloadTicket(
      "token",
      opaqueId,
      "fresh-export-download-proof",
    )).rejects.toThrow("invalid backup export ticket");
  });

  it.each([
    `https://example.invalid/api/v1/asset-content/${opaqueId}`,
    `/api/v1/asset-content/${opaqueId}?ticket=secret`,
  ])("rejects a non-canonical export ticket URL: %s", async (contentUrl) => {
    const api = createBackupExportsApi(vi.fn().mockResolvedValue(rawExportTicket(contentUrl)));
    await expect(api.issueDownloadTicket(
      "token",
      opaqueId,
      "fresh-export-download-proof",
    )).rejects.toThrow("invalid backup export ticket");
  });
});
