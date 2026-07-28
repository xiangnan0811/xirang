import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { ApiError } from "@/lib/api/core";
import type { AssetRef, BackupExportJob } from "@/types/domain";

import {
  useBackupAssetExport,
  type BackupAssetExportApi,
  type BackupAssetExportCreateOptions,
} from "./use-backup-asset-export";

const ref: AssetRef = { recoveryPointId: "1".repeat(32), entryId: "2".repeat(64) };
const jobId = "3".repeat(32);
const digest = "4".repeat(64);
const zipCreateOptions = {
  archiveFormat: "zip",
  archiveProfile: "zip_deflate_v1",
} as const satisfies BackupAssetExportCreateOptions;

function job(overrides: Partial<BackupExportJob> = {}): BackupExportJob {
  return {
    schemaVersion: 1,
    id: jobId,
    selectionDigest: digest,
    archiveFormat: "zip",
    archiveProfile: "zip_deflate_v1",
    executionState: "queued",
    resultKind: null,
    cleanupState: "none",
    itemCount: 1,
    packedCount: 0,
    skippedCount: 0,
    failedCount: 0,
    logicalBytes: 77,
    providerBytes: 0,
    artifactBytes: 0,
    errorCategory: null,
    createdAt: new Date(Date.now() - 1_000).toISOString(),
    absoluteDeadline: new Date(Date.now() + 60_000).toISOString(),
    readyAt: null,
    expiresAt: null,
    attempt: null,
    items: [{ id: "5".repeat(32), ordinal: 0, state: "pending", logicalBytes: 0, providerBytes: 0, errorCategory: null }],
    nextCursor: null,
    pollAfterSeconds: 1,
    canCancel: true,
    canDownload: false,
    ...overrides,
  };
}

function readyJob(overrides: Partial<BackupExportJob> = {}): BackupExportJob {
  const expiresAt = new Date(Date.now() + 60_000).toISOString();
  return job({
    executionState: "ready",
    resultKind: "complete",
    packedCount: 1,
    logicalBytes: 1234,
    providerBytes: 700,
    artifactBytes: 812,
    readyAt: new Date(Date.now() - 1_000).toISOString(),
    expiresAt,
    canCancel: true,
    canDownload: true,
    pollAfterSeconds: 0,
    items: [{ id: "5".repeat(32), ordinal: 0, state: "packed", logicalBytes: 1234, providerBytes: 700, errorCategory: null }],
    ...overrides,
  });
}

function baseOptions(api: BackupAssetExportApi, onRouteChange = vi.fn()) {
  return {
    token: "token",
    role: "admin" as const,
    ensureStepUpProof: vi.fn().mockResolvedValue("fresh-proof"),
    onRouteChange,
    api,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function setDocumentHidden(hidden: boolean, notify = true) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => (hidden ? "hidden" : "visible"),
  });
  if (notify) document.dispatchEvent(new Event("visibilitychange"));
}

function setOnline(online: boolean, notify = true) {
  Object.defineProperty(navigator, "onLine", {
    configurable: true,
    get: () => online,
  });
  if (notify) window.dispatchEvent(new Event(online ? "online" : "offline"));
}

describe("useBackupAssetExport", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
    Object.defineProperty(navigator, "onLine", { configurable: true, value: true });
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  });

  it("uses a one-time create proof and replaces estimates with authoritative job totals", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: job(), replay: false }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const options = baseOptions(api, onRouteChange);
    const { result } = renderHook(() => useBackupAssetExport(options));

    act(() => result.current.open([ref], { count: 99, logicalBytes: 9999 }));
    await act(async () => result.current.create(zipCreateOptions));

    expect(options.ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.assetExportCreate, {
      persist: false,
      reuseCached: false,
    });
    expect(api.create).toHaveBeenCalledWith(
      "token",
      expect.objectContaining({ selection: { schemaVersion: 1, kind: "explicit", refs: [ref] } }),
      "fresh-proof",
      expect.any(AbortSignal),
    );
    expect(result.current.state.estimate).toEqual({ count: 1, logicalBytes: 77 });
    expect(result.current.state.job?.selectionDigest).toBe(digest);
    expect(onRouteChange).toHaveBeenCalledWith(jobId, { replace: false });
  });

  it("polls at the server cadence and stops after a terminal ready result", async () => {
    vi.useFakeTimers();
    const api: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: job(), replay: false }),
      status: vi.fn().mockResolvedValue(readyJob()),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 1 }));
    await act(async () => result.current.create(zipCreateOptions));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(999);
    });
    expect(api.status).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(api.status).toHaveBeenCalledWith("token", jobId, expect.objectContaining({ limit: 100, signal: expect.any(AbortSignal) }));
    expect(result.current.state.phase).toBe("terminal");
    expect(result.current.state.job?.executionState).toBe("ready");
  });

  it("continues polling a cancel request until the server closes it", async () => {
    vi.useFakeTimers();
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue(job({
        executionState: "canceled",
        pollAfterSeconds: 0,
        canCancel: false,
      })),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(job()));

    await act(async () => result.current.cancel());
    expect(result.current.state.job?.executionState).toBe("cancel_requested");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(api.status).toHaveBeenCalledWith(
      "token",
      jobId,
      expect.objectContaining({ limit: 100, signal: expect.any(AbortSignal) }),
    );
    expect(result.current.state.job?.executionState).toBe("canceled");
  });

  it("reconciles an opaque direct-route job with an immediate status GET", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue(readyJob()),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      exportJobId: jobId,
    }));

    await waitFor(() => expect(result.current.state.job?.id).toBe(jobId));

    expect(api.status).toHaveBeenCalledWith("token", jobId, expect.objectContaining({
      limit: 100,
      signal: expect.any(AbortSignal),
    }));
    expect(onRouteChange).not.toHaveBeenCalled();
  });

  it("reloads a direct-route job after its route is removed and revisited", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue(readyJob()),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const options = baseOptions(api);
    const { result, rerender } = renderHook(
      ({ exportJobId }) => useBackupAssetExport({ ...options, exportJobId }),
      { initialProps: { exportJobId: jobId as string | undefined } },
    );

    await waitFor(() => expect(api.status).toHaveBeenCalledTimes(1));
    expect(result.current.state.job?.id).toBe(jobId);

    await act(async () => {
      result.current.dismiss();
      rerender({ exportJobId: undefined });
      await Promise.resolve();
    });
    await act(async () => {
      rerender({ exportJobId: jobId });
      await Promise.resolve();
    });

    await waitFor(() => expect(api.status).toHaveBeenCalledTimes(2));
    expect(result.current.state.job?.id).toBe(jobId);
  });

  it("drops an aborted direct-route status result after the route changes jobs", async () => {
    const firstJobId = "a".repeat(32);
    const secondJobId = "b".repeat(32);
    const firstStatus = deferred<BackupExportJob>();
    const secondStatus = deferred<BackupExportJob>();
    const signals: AbortSignal[] = [];
    const status = vi.fn((_: string, requestedJobId: string, requestOptions: { signal?: AbortSignal }) => {
      if (requestOptions.signal) signals.push(requestOptions.signal);
      return requestedJobId === firstJobId ? firstStatus.promise : secondStatus.promise;
    });
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status,
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const options = baseOptions(api);
    const { result, rerender } = renderHook(
      ({ exportJobId }) => useBackupAssetExport({ ...options, exportJobId }),
      { initialProps: { exportJobId: firstJobId } },
    );

    await waitFor(() => expect(status).toHaveBeenCalledTimes(1));
    rerender({ exportJobId: secondJobId });
    await waitFor(() => expect(status).toHaveBeenCalledTimes(2));
    expect(signals[0]?.aborted).toBe(true);

    await act(async () => {
      secondStatus.resolve(job({
        id: secondJobId,
        executionState: "canceled",
        canCancel: false,
        pollAfterSeconds: 0,
      }));
      await Promise.resolve();
    });
    await waitFor(() => expect(result.current.state.job?.id).toBe(secondJobId));

    await act(async () => {
      firstStatus.resolve(job({
        id: firstJobId,
        executionState: "canceled",
        canCancel: false,
        pollAfterSeconds: 0,
      }));
      await Promise.resolve();
    });

    expect(result.current.state.phase).toBe("terminal");
    expect(result.current.state.job?.id).toBe(secondJobId);
    expect(status).toHaveBeenCalledTimes(2);
  });

  it("replace-repairs a missing direct-route job without leaking server detail", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockRejectedValue(new ApiError(404, "raw /private/export/path")),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      exportJobId: jobId,
    }));

    await waitFor(() => expect(result.current.state.error).toBe("not_found"));

    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
    expect(JSON.stringify(result.current.state)).not.toContain("private/export");
  });

  it("replace-clears an opaque direct-route job after status returns 401", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockRejectedValue(new ApiError(401, "expired session detail")),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      exportJobId: jobId,
    }));

    await waitFor(() => expect(result.current.state.error).toBe("forbidden"));

    expect(result.current.state).toMatchObject({ phase: "error", job: null, ticket: null });
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
    expect(JSON.stringify(result.current.state)).not.toContain("expired session");
  });

  it("replace-clears the job route when cancel is no longer authorized", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn(),
      cancel: vi.fn().mockRejectedValue(new ApiError(403, "forbidden")),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api, onRouteChange)));
    act(() => result.current.hydrate(job()));

    await act(async () => result.current.cancel());

    expect(result.current.state).toMatchObject({ phase: "error", error: "forbidden", job: null, ticket: null });
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it("keeps polling an expiring job until the server closes it as expired", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const expiring = job({
      executionState: "expiring",
      resultKind: "complete",
      packedCount: 1,
      artifactBytes: 812,
      readyAt: new Date(Date.now() - 1_000).toISOString(),
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      canCancel: false,
      canDownload: false,
      pollAfterSeconds: 1,
      items: [{ id: "5".repeat(32), ordinal: 0, state: "packed", logicalBytes: 77, providerBytes: 77, errorCategory: null }],
    });
    const expired = { ...expiring, executionState: "expired", cleanupState: "purged", pollAfterSeconds: 0 } as const;
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue(expired),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));

    act(() => result.current.hydrate(expiring));
    await act(async () => vi.advanceTimersByTimeAsync(1_000));

    expect(api.status).toHaveBeenCalledWith("token", jobId, expect.objectContaining({ limit: 100 }));
    expect(result.current.state.job?.executionState).toBe("expired");
    expect(result.current.state.phase).toBe("terminal");
  });

  it("takes a fresh download proof and clears the in-memory ticket after handoff", async () => {
    const ticket = {
      schemaVersion: 1 as const,
      contentUrl: `/api/v1/asset-content/${jobId}`,
      contentType: "application/zip",
      contentLength: 812,
      etag: '"export-etag"',
      range: "single" as const,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      idleExpiresAt: new Date(Date.now() + 30_000).toISOString(),
    };
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn().mockResolvedValue(ticket),
    };
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const options = baseOptions(api);
    const { result } = renderHook(() => useBackupAssetExport(options));
    act(() => result.current.hydrate(readyJob()));
    await act(async () => result.current.download());

    expect(options.ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.assetExportDownload, {
      persist: false,
      reuseCached: false,
    });
    expect(api.issueDownloadTicket).toHaveBeenCalledWith("token", jobId, "fresh-proof", expect.any(AbortSignal));
    expect(result.current.state.ticket).toBeNull();
  });

  it("replace-clears the job route when ticket issuance cannot find the artifact", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn().mockRejectedValue(new ApiError(404, "not found")),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api, onRouteChange)));
    act(() => result.current.hydrate(readyJob()));

    await act(async () => result.current.download());

    expect(result.current.state).toMatchObject({ phase: "error", error: "not_found", job: null, ticket: null });
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it.each([
    ["zip", "zip_deflate_v1", "application/zip", ".zip"],
    ["tar", "tar_none_v1", "application/x-tar", ".tar"],
    ["tar", "tar_gzip_v1", "application/gzip", ".tar.gz"],
  ] as const)("uses the closed %s/%s browser download suffix", async (
    archiveFormat,
    archiveProfile,
    contentType,
    suffix,
  ) => {
    const ticket = {
      schemaVersion: 1 as const,
      contentUrl: `/api/v1/asset-content/${jobId}`,
      contentType,
      contentLength: 812,
      etag: '"export-etag"',
      range: "single" as const,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      idleExpiresAt: new Date(Date.now() + 30_000).toISOString(),
    };
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn().mockResolvedValue(ticket),
    };
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(readyJob({
      archiveFormat,
      archiveProfile,
    })));

    await act(async () => result.current.download());

    const clickedAnchor = click.mock.instances[click.mock.instances.length - 1];
    expect(clickedAnchor).toBeInstanceOf(HTMLAnchorElement);
    if (!(clickedAnchor instanceof HTMLAnchorElement)) throw new Error("download anchor was not clicked");
    expect(clickedAnchor.download).toBe(`xirang-export-${jobId.slice(0, 16)}${suffix}`);
  });

  it("retries a transient create with the same idempotency key", async () => {
    vi.useFakeTimers();
    const api: BackupAssetExportApi = {
      create: vi.fn()
        .mockRejectedValueOnce(new ApiError(503, "unavailable", undefined, 1))
        .mockResolvedValueOnce({ job: job(), replay: true }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockResolvedValueOnce("retry-create-proof");
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      ensureStepUpProof,
    }));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(api.create).toHaveBeenCalledTimes(2);
    expect(ensureStepUpProof).toHaveBeenNthCalledWith(1, STEP_UP_ACTIONS.assetExportCreate, {
      persist: false,
      reuseCached: false,
    });
    expect(ensureStepUpProof).toHaveBeenNthCalledWith(2, STEP_UP_ACTIONS.assetExportCreate, {
      persist: false,
      reuseCached: false,
    });
    expect(vi.mocked(api.create).mock.calls[0]?.[2]).toBe("initial-create-proof");
    expect(vi.mocked(api.create).mock.calls[1]?.[2]).toBe("retry-create-proof");
    expect(vi.mocked(api.create).mock.calls[1]?.[1]).toBe(vi.mocked(api.create).mock.calls[0]?.[1]);
    expect(vi.mocked(api.create).mock.calls[1]?.[1]?.idempotencyKey).toBe(
      vi.mocked(api.create).mock.calls[0]?.[1]?.idempotencyKey,
    );
    const initialSignal = vi.mocked(api.create).mock.calls[0]?.[3];
    const retrySignal = vi.mocked(api.create).mock.calls[1]?.[3];
    expect(initialSignal).toBeInstanceOf(AbortSignal);
    expect(retrySignal).toBe(initialSignal);
    expect(initialSignal?.aborted).toBe(false);
    expect(retrySignal?.aborted).toBe(false);
    expect(onRouteChange).toHaveBeenCalledWith(jobId, { replace: false });
  });

  it.each([
    ["a network failure", () => new TypeError("network unavailable")],
    ["an ambiguous 500 response", () => new ApiError(500, "export create outcome unavailable")],
  ])("automatically replays a live create with the same idempotency key after %s", async (_description, failure) => {
    const api: BackupAssetExportApi = {
      create: vi.fn()
        .mockRejectedValueOnce(failure())
        .mockResolvedValueOnce({ job: job(), replay: true }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockResolvedValueOnce("replay-create-proof");
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      ensureStepUpProof,
    }));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));

    await act(async () => result.current.create(zipCreateOptions));

    expect(api.create).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.create).mock.calls[1]?.[1]).toBe(vi.mocked(api.create).mock.calls[0]?.[1]);
    expect(vi.mocked(api.create).mock.calls[1]?.[1]?.idempotencyKey).toBe(
      vi.mocked(api.create).mock.calls[0]?.[1]?.idempotencyKey,
    );
    expect(vi.mocked(api.create).mock.calls[1]?.[2]).toBe("replay-create-proof");
    expect(result.current.state).toMatchObject({ phase: "active", error: null, job: { id: jobId } });
    expect(onRouteChange).toHaveBeenCalledWith(jobId, { replace: false });
  });

  it("replays the original frozen export intent after ambiguous retries are exhausted", async () => {
    const changedRef: AssetRef = { recoveryPointId: "6".repeat(32), entryId: "7".repeat(64) };
    let attempts = 0;
    const api: BackupAssetExportApi = {
      create: vi.fn(() => {
        attempts += 1;
        return attempts <= 4
          ? Promise.reject(new ApiError(500, "export create outcome unavailable"))
          : Promise.resolve({ job: job(), replay: true });
      }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));

    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));

    expect(api.create).toHaveBeenCalledTimes(4);
    expect(result.current.state.error).toBe("unavailable");
    const frozenInput = vi.mocked(api.create).mock.calls[0]?.[1];

    act(() => result.current.open([changedRef], { count: 1, logicalBytes: 1 }));
    await act(async () => result.current.create({ archiveFormat: "tar", archiveProfile: "tar_gzip_v1" }));

    expect(api.create).toHaveBeenCalledTimes(5);
    for (const [, input] of vi.mocked(api.create).mock.calls) {
      expect(input).toBe(frozenInput);
    }
    expect(frozenInput).toMatchObject({
      selection: { schemaVersion: 1, kind: "explicit", refs: [ref] },
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
    });
    expect(result.current.state.job?.id).toBe(jobId);
  });

  it("uses a new intent when the first create proof fails before transport", async () => {
    const changedRef: AssetRef = { recoveryPointId: "6".repeat(32), entryId: "7".repeat(64) };
    const randomUUID = vi.spyOn(crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000001")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000002");
    const api: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: job(), replay: false }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockRejectedValueOnce(new ApiError(403, "create proof rejected"))
      .mockResolvedValueOnce("later-create-proof");
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api),
      ensureStepUpProof,
    }));

    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));
    expect(api.create).not.toHaveBeenCalled();

    act(() => result.current.open([changedRef], { count: 1, logicalBytes: 1 }));
    await act(async () => result.current.create({ archiveFormat: "tar", archiveProfile: "tar_gzip_v1" }));

    expect(randomUUID).toHaveBeenCalledTimes(2);
    expect(api.create).toHaveBeenCalledWith(
      "token",
      expect.objectContaining({
        selection: { schemaVersion: 1, kind: "explicit", refs: [changedRef] },
        archiveFormat: "tar",
        archiveProfile: "tar_gzip_v1",
        idempotencyKey: "export-00000000-0000-4000-8000-000000000002",
      }),
      "later-create-proof",
      expect.any(AbortSignal),
    );
  });

  it("uses a new intent when a definitive 429 retry loses its fresh proof", async () => {
    vi.useFakeTimers();
    const changedRef: AssetRef = { recoveryPointId: "6".repeat(32), entryId: "7".repeat(64) };
    const randomUUID = vi.spyOn(crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000003")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000004");
    const api: BackupAssetExportApi = {
      create: vi.fn()
        .mockRejectedValueOnce(new ApiError(429, "rate limited", undefined, 1))
        .mockResolvedValueOnce({ job: job(), replay: false }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockRejectedValueOnce(new ApiError(403, "retry create proof rejected"))
      .mockResolvedValueOnce("later-create-proof");
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api),
      ensureStepUpProof,
    }));

    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));
    expect(api.create).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(api.create).toHaveBeenCalledTimes(1);

    act(() => result.current.open([changedRef], { count: 1, logicalBytes: 1 }));
    await act(async () => result.current.create({ archiveFormat: "tar", archiveProfile: "tar_gzip_v1" }));

    expect(randomUUID).toHaveBeenCalledTimes(2);
    expect(api.create).toHaveBeenCalledWith(
      "token",
      expect.objectContaining({
        selection: { schemaVersion: 1, kind: "explicit", refs: [changedRef] },
        archiveFormat: "tar",
        archiveProfile: "tar_gzip_v1",
        idempotencyKey: "export-00000000-0000-4000-8000-000000000004",
      }),
      "later-create-proof",
      expect.any(AbortSignal),
    );
  });

  it("does not reconcile a dismissed definitive 429 create backoff", async () => {
    vi.useFakeTimers();
    const api: BackupAssetExportApi = {
      create: vi.fn()
        .mockRejectedValueOnce(new ApiError(429, "rate limited", undefined, 1))
        .mockResolvedValueOnce({ job: job(), replay: false }),
      status: vi.fn(),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockResolvedValueOnce("must-not-be-requested");
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      ensureStepUpProof,
    }));

    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));
    expect(api.create).toHaveBeenCalledTimes(1);

    act(() => result.current.dismiss());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(api.create).toHaveBeenCalledTimes(1);
    expect(api.cancel).not.toHaveBeenCalled();
    expect(result.current.state).toMatchObject({ phase: "closed", job: null });
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it("reconciles a dismissed ambiguous 503 create backoff with its frozen intent", async () => {
    vi.useFakeTimers();
    const api: BackupAssetExportApi = {
      create: vi.fn()
        .mockRejectedValueOnce(new ApiError(503, "export create outcome unavailable", undefined, 1))
        .mockResolvedValueOnce({ job: job(), replay: true }),
      status: vi.fn(),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockResolvedValueOnce("reconciliation-create-proof");
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      ensureStepUpProof,
    }));

    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));
    const frozenInput = vi.mocked(api.create).mock.calls[0]?.[1];
    expect(api.create).toHaveBeenCalledTimes(1);

    act(() => result.current.dismiss());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(ensureStepUpProof).toHaveBeenCalledTimes(2);
    expect(api.create).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.create).mock.calls[1]?.[1]).toBe(frozenInput);
    expect(vi.mocked(api.create).mock.calls[1]?.[1]?.idempotencyKey).toBe(frozenInput?.idempotencyKey);
    expect(api.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
    expect(result.current.state).toMatchObject({ phase: "closed", job: null });
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it.each([
    ["reconciles a 503 followed by a retry 429", true],
    ["does not reconcile a plain 429", false],
  ] as const)("%s create backoff after dismissal", async (_description, startedAmbiguous) => {
    vi.useFakeTimers();
    const api: BackupAssetExportApi = {
      create: vi.fn()
        .mockRejectedValueOnce(startedAmbiguous
          ? new ApiError(503, "export create outcome unavailable", undefined, 1)
          : new ApiError(429, "rate limited", undefined, 1))
        .mockRejectedValueOnce(new ApiError(429, "rate limited", undefined, 1))
        .mockResolvedValueOnce({ job: job(), replay: true }),
      status: vi.fn(),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockResolvedValueOnce("retry-create-proof")
      .mockResolvedValueOnce("reconciliation-create-proof");
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      ensureStepUpProof,
    }));

    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));
    const frozenInput = vi.mocked(api.create).mock.calls[0]?.[1];

    if (startedAmbiguous) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_000);
      });
      expect(api.create).toHaveBeenCalledTimes(2);
      expect(vi.mocked(api.create).mock.calls[1]?.[1]).toBe(frozenInput);
    } else {
      expect(api.create).toHaveBeenCalledTimes(1);
    }

    act(() => result.current.dismiss());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    if (startedAmbiguous) {
      expect(ensureStepUpProof).toHaveBeenCalledTimes(3);
      expect(api.create).toHaveBeenCalledTimes(3);
      expect(vi.mocked(api.create).mock.calls[2]?.[1]).toBe(frozenInput);
      expect(vi.mocked(api.create).mock.calls[2]?.[1]?.idempotencyKey).toBe(frozenInput?.idempotencyKey);
      expect(api.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
    } else {
      expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
      expect(api.create).toHaveBeenCalledTimes(1);
      expect(api.cancel).not.toHaveBeenCalled();
    }
    expect(result.current.state).toMatchObject({ phase: "closed", job: null });
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it.each([
    ["a definitive 429", "resolves", false],
    ["a definitive 429", "rejects", false],
    ["an ambiguous 503", "resolves", true],
    ["an ambiguous 503", "rejects", true],
  ] as const)(
    "preserves %s cancellation provenance when its timer-fired retry proof %s",
    async (_description, proofOutcome, ambiguous) => {
      vi.useFakeTimers();
      const retryProof = deferred<string>();
      const api: BackupAssetExportApi = {
        create: vi.fn()
          .mockRejectedValueOnce(ambiguous
            ? new ApiError(503, "export create outcome unavailable", undefined, 1)
            : new ApiError(429, "rate limited", undefined, 1))
          .mockResolvedValueOnce({ job: job(), replay: true }),
        status: vi.fn(),
        cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
        issueDownloadTicket: vi.fn(),
      };
      const ensureStepUpProof = vi.fn()
        .mockResolvedValueOnce("initial-create-proof")
        .mockReturnValueOnce(retryProof.promise)
        .mockResolvedValueOnce("reconciliation-create-proof");
      const onRouteChange = vi.fn();
      const { result } = renderHook(() => useBackupAssetExport({
        ...baseOptions(api, onRouteChange),
        ensureStepUpProof,
      }));

      act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
      await act(async () => result.current.create(zipCreateOptions));
      const frozenInput = vi.mocked(api.create).mock.calls[0]?.[1];
      expect(api.create).toHaveBeenCalledTimes(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_000);
      });
      expect(ensureStepUpProof).toHaveBeenCalledTimes(2);

      act(() => result.current.dismiss());
      await act(async () => {
        if (proofOutcome === "resolves") retryProof.resolve("retry-create-proof");
        else retryProof.reject(new ApiError(403, "retry create proof rejected"));
        await Promise.resolve();
        await Promise.resolve();
        await Promise.resolve();
        await Promise.resolve();
        await Promise.resolve();
      });

      if (ambiguous) {
        expect(ensureStepUpProof).toHaveBeenCalledTimes(3);
        expect(api.create).toHaveBeenCalledTimes(2);
        expect(vi.mocked(api.create).mock.calls[1]?.[1]).toBe(frozenInput);
        expect(vi.mocked(api.create).mock.calls[1]?.[1]?.idempotencyKey).toBe(frozenInput?.idempotencyKey);
        expect(api.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
      } else {
        expect(ensureStepUpProof).toHaveBeenCalledTimes(2);
        expect(api.create).toHaveBeenCalledTimes(1);
        expect(api.cancel).not.toHaveBeenCalled();
      }
      expect(result.current.state).toMatchObject({ phase: "closed", job: null });
      expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
    },
  );

  it("keeps the frozen intent after an ambiguous create is followed by a proof failure", async () => {
    const changedRef: AssetRef = { recoveryPointId: "6".repeat(32), entryId: "7".repeat(64) };
    let createCalls = 0;
    const api: BackupAssetExportApi = {
      create: vi.fn(() => {
        createCalls += 1;
        return createCalls === 1
          ? Promise.reject(new ApiError(500, "export create outcome unavailable"))
          : Promise.resolve({ job: job(), replay: true });
      }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockRejectedValueOnce(new ApiError(403, "create proof rejected"))
      .mockResolvedValueOnce("later-create-proof");
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api),
      ensureStepUpProof,
    }));

    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));

    expect(api.create).toHaveBeenCalledTimes(1);
    const frozenInput = vi.mocked(api.create).mock.calls[0]?.[1];

    act(() => result.current.open([changedRef], { count: 1, logicalBytes: 1 }));
    await act(async () => result.current.create({ archiveFormat: "tar", archiveProfile: "tar_gzip_v1" }));

    expect(api.create).toHaveBeenCalledTimes(2);
    const replayInput = vi.mocked(api.create).mock.calls[1]?.[1];
    expect(replayInput).toBe(frozenInput);
    expect(replayInput).toMatchObject({
      selection: { schemaVersion: 1, kind: "explicit", refs: [ref] },
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
    });
    expect(replayInput?.idempotencyKey).toBe(frozenInput?.idempotencyKey);
  });

  it("reconciles the frozen ambiguous intent across an actual unmount and remount", async () => {
    vi.useFakeTimers();
    const changedRef: AssetRef = { recoveryPointId: "6".repeat(32), entryId: "7".repeat(64) };
    const remountedJobId = "8".repeat(32);
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    let createCalls = 0;
    const api: BackupAssetExportApi = {
      create: vi.fn(() => {
        createCalls += 1;
        if (createCalls === 1) {
          return Promise.reject(new ApiError(500, "export create outcome unavailable"));
        }
        return Promise.resolve({
          job: job({ id: createCalls === 2 ? jobId : remountedJobId }),
          replay: createCalls === 2,
        });
      }),
      status: vi.fn(),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockRejectedValueOnce(new ApiError(403, "ambiguous retry proof rejected"))
      .mockResolvedValueOnce("teardown-reconciliation-proof")
      .mockResolvedValueOnce("remounted-create-proof");
    const onRouteChange = vi.fn();
    const options = {
      ...baseOptions(api, onRouteChange),
      ensureStepUpProof,
    };
    const firstMount = renderHook(() => useBackupAssetExport(options));
    act(() => firstMount.result.current.open([ref], { count: 1, logicalBytes: 77 }));

    await act(async () => firstMount.result.current.create(zipCreateOptions));

    expect(api.create).toHaveBeenCalledTimes(1);
    const frozenInput = vi.mocked(api.create).mock.calls[0]?.[1];
    firstMount.unmount();
    const secondMount = renderHook(() => useBackupAssetExport(options));

    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.create).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.create).mock.calls[1]?.[1]).toBe(frozenInput);
    expect(vi.mocked(api.create).mock.calls[1]?.[1]?.idempotencyKey).toBe(frozenInput?.idempotencyKey);
    expect(api.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
    expect(onRouteChange).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();

    act(() => secondMount.result.current.open([changedRef], { count: 1, logicalBytes: 1 }));
    await act(async () => secondMount.result.current.create({ archiveFormat: "tar", archiveProfile: "tar_gzip_v1" }));

    const remountedInput = vi.mocked(api.create).mock.calls[2]?.[1];
    expect(remountedInput).not.toBe(frozenInput);
    expect(remountedInput).toMatchObject({
      selection: { schemaVersion: 1, kind: "explicit", refs: [changedRef] },
      archiveFormat: "tar",
      archiveProfile: "tar_gzip_v1",
    });
    expect(remountedInput?.idempotencyKey).not.toBe(frozenInput?.idempotencyKey);
    expect(onRouteChange).toHaveBeenCalledWith(remountedJobId, { replace: false });
    secondMount.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("keeps the frozen intent when cancellation reconciliation cannot obtain a proof", async () => {
    const changedRef: AssetRef = { recoveryPointId: "6".repeat(32), entryId: "7".repeat(64) };
    const firstCreate = deferred<Awaited<ReturnType<BackupAssetExportApi["create"]>>>();
    let createCalls = 0;
    const api: BackupAssetExportApi = {
      create: vi.fn(() => {
        createCalls += 1;
        return createCalls === 1
          ? firstCreate.promise
          : Promise.resolve({ job: job(), replay: true });
      }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockRejectedValueOnce(new ApiError(403, "reconciliation proof rejected"))
      .mockResolvedValueOnce("later-create-proof");
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api),
      ensureStepUpProof,
    }));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));

    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(zipCreateOptions);
    });
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    const frozenInput = vi.mocked(api.create).mock.calls[0]?.[1];

    act(() => result.current.dismiss());
    firstCreate.reject(new ApiError(500, "export create outcome unavailable"));
    await act(async () => {
      await creating;
    });

    expect(api.create).toHaveBeenCalledTimes(1);
    act(() => result.current.open([changedRef], { count: 1, logicalBytes: 1 }));
    await act(async () => result.current.create({ archiveFormat: "tar", archiveProfile: "tar_gzip_v1" }));

    expect(api.create).toHaveBeenCalledTimes(2);
    const replayInput = vi.mocked(api.create).mock.calls[1]?.[1];
    expect(replayInput).toBe(frozenInput);
    expect(replayInput).toMatchObject({
      selection: { schemaVersion: 1, kind: "explicit", refs: [ref] },
      archiveFormat: "zip",
      archiveProfile: "zip_deflate_v1",
    });
    expect(replayInput?.idempotencyKey).toBe(frozenInput?.idempotencyKey);
  });

  it("cancels one late durable create after dismissal without restoring the export route", async () => {
    const pendingCreate = deferred<Awaited<ReturnType<BackupAssetExportApi["create"]>>>();
    let createSignal: AbortSignal | undefined;
    const api: BackupAssetExportApi = {
      create: vi.fn((_token, _input, _proof, signal) => {
        createSignal = signal;
        return pendingCreate.promise;
      }),
      status: vi.fn(),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api, onRouteChange)));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));

    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(zipCreateOptions);
    });
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));

    act(() => result.current.dismiss());
    expect(createSignal?.aborted).toBe(true);

    pendingCreate.resolve({ job: job(), replay: false });
    await act(async () => {
      await creating;
    });

    expect(api.cancel).toHaveBeenCalledTimes(1);
    expect(api.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
    expect(result.current.state).toMatchObject({ phase: "closed", job: null });
    expect(onRouteChange).toHaveBeenCalledTimes(1);
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it.each([
    ["an abort", () => new DOMException("export create aborted", "AbortError")],
    ["a 5xx response", () => new ApiError(503, "export create outcome unavailable")],
  ])("replays a dismissed create with the same idempotency key after %s and cancels it", async (_description, error) => {
    const pendingCreate = deferred<Awaited<ReturnType<BackupAssetExportApi["create"]>>>();
    const idempotencyKeys: string[] = [];
    const signals: AbortSignal[] = [];
    let createCalls = 0;
    const api: BackupAssetExportApi = {
      create: vi.fn((_token, input, _proof, signal) => {
        createCalls += 1;
        idempotencyKeys.push(input.idempotencyKey);
        if (signal) signals.push(signal);
        return createCalls === 1
          ? pendingCreate.promise
          : Promise.resolve({ job: job(), replay: true });
      }),
      status: vi.fn(),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const ensureStepUpProof = vi.fn()
      .mockResolvedValueOnce("initial-create-proof")
      .mockResolvedValueOnce("reconciliation-create-proof");
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api, onRouteChange),
      ensureStepUpProof,
    }));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));

    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(zipCreateOptions);
    });
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));

    act(() => result.current.dismiss());
    expect(signals[0]?.aborted).toBe(true);

    pendingCreate.reject(error());
    await act(async () => {
      await creating;
    });

    expect(api.create).toHaveBeenCalledTimes(2);
    expect(idempotencyKeys[1]).toBe(idempotencyKeys[0]);
    expect(ensureStepUpProof).toHaveBeenNthCalledWith(1, STEP_UP_ACTIONS.assetExportCreate, {
      persist: false,
      reuseCached: false,
    });
    expect(ensureStepUpProof).toHaveBeenNthCalledWith(2, STEP_UP_ACTIONS.assetExportCreate, {
      persist: false,
      reuseCached: false,
    });
    expect(vi.mocked(api.create).mock.calls[0]?.[2]).toBe("initial-create-proof");
    expect(vi.mocked(api.create).mock.calls[1]?.[2]).toBe("reconciliation-create-proof");
    expect(vi.mocked(api.create).mock.calls[1]?.[1]).toBe(vi.mocked(api.create).mock.calls[0]?.[1]);
    expect(signals[1]?.aborted).toBe(false);
    expect(api.cancel).toHaveBeenCalledTimes(1);
    expect(api.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
    expect(result.current.state).toMatchObject({ phase: "closed", job: null });
    expect(onRouteChange).toHaveBeenCalledTimes(1);
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it("does not replay a dismissed create after a definitive API rejection", async () => {
    const pendingCreate = deferred<Awaited<ReturnType<BackupAssetExportApi["create"]>>>();
    let createCalls = 0;
    const api: BackupAssetExportApi = {
      create: vi.fn(() => {
        createCalls += 1;
        return createCalls === 1
          ? pendingCreate.promise
          : Promise.resolve({ job: job(), replay: true });
      }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api, onRouteChange)));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));

    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(zipCreateOptions);
    });
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));

    act(() => result.current.dismiss());
    pendingCreate.reject(new ApiError(400, "invalid export request"));
    await act(async () => {
      await creating;
    });

    expect(api.create).toHaveBeenCalledTimes(1);
    expect(api.cancel).not.toHaveBeenCalled();
    expect(result.current.state).toMatchObject({ phase: "closed", job: null });
    expect(onRouteChange).toHaveBeenCalledTimes(1);
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it("reconciles a pending create before loading a replacement direct-route job", async () => {
    const pendingCreate = deferred<Awaited<ReturnType<BackupAssetExportApi["create"]>>>();
    const replacementJobId = "6".repeat(32);
    const api: BackupAssetExportApi = {
      create: vi.fn().mockReturnValue(pendingCreate.promise),
      status: vi.fn().mockResolvedValue(job({
        id: replacementJobId,
        executionState: "canceled",
        canCancel: false,
        pollAfterSeconds: 0,
      })),
      cancel: vi.fn().mockResolvedValue(job({ executionState: "cancel_requested" })),
      issueDownloadTicket: vi.fn(),
    };
    const options = baseOptions(api);
    const { result, rerender } = renderHook(
      ({ exportJobId }) => useBackupAssetExport({ ...options, exportJobId }),
      { initialProps: { exportJobId: undefined as string | undefined } },
    );
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));

    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(zipCreateOptions);
    });
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));

    try {
      rerender({ exportJobId: replacementJobId });
      await waitFor(() => expect(api.status).toHaveBeenCalledWith(
        "token",
        replacementJobId,
        expect.objectContaining({ limit: 100, signal: expect.any(AbortSignal) }),
      ));
      expect(result.current.state.job?.id).toBe(replacementJobId);
    } finally {
      pendingCreate.resolve({ job: job(), replay: false });
      await act(async () => {
        await creating;
      });
    }

    expect(api.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
    expect(result.current.state.job?.id).toBe(replacementJobId);
  });

  it.each([429, 503])("pauses a scheduled %i status retry until online and visible", async (statusCode) => {
    vi.useFakeTimers();
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn()
        .mockRejectedValueOnce(new ApiError(statusCode, "unavailable", undefined, 1))
        .mockResolvedValueOnce(job({
          executionState: "canceled",
          canCancel: false,
          pollAfterSeconds: 0,
        })),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport({
      ...baseOptions(api),
      exportJobId: jobId,
    }));

    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(api.status).toHaveBeenCalledTimes(1);
    setOnline(false, false);
    setDocumentHidden(true, false);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(api.status).toHaveBeenCalledTimes(1);

    await act(async () => {
      setOnline(true);
      await Promise.resolve();
    });
    expect(api.status).toHaveBeenCalledTimes(1);

    await act(async () => {
      setDocumentHidden(false);
      await Promise.resolve();
    });
    expect(api.status).toHaveBeenCalledTimes(2);
    expect(result.current.state.job?.executionState).toBe("canceled");
  });

  it.each([429, 503])("fences a scheduled %i retry when the route changes from job A to job B", async (statusCode) => {
    vi.useFakeTimers();
    const firstJobId = "a".repeat(32);
    const secondJobId = "b".repeat(32);
    const secondStatus = deferred<BackupExportJob>();
    let secondSignal: AbortSignal | undefined;
    const status = vi.fn((_token: string, requestedJobId: string, requestOptions: { signal?: AbortSignal }) => {
      if (requestedJobId === firstJobId) {
        return Promise.reject(new ApiError(statusCode, "unavailable", undefined, 1));
      }
      secondSignal = requestOptions.signal;
      return secondStatus.promise;
    });
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status,
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const options = baseOptions(api);
    const { result, rerender } = renderHook(
      ({ exportJobId }) => useBackupAssetExport({ ...options, exportJobId }),
      { initialProps: { exportJobId: firstJobId } },
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(status).toHaveBeenCalledTimes(1);

    rerender({ exportJobId: secondJobId });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(status).toHaveBeenCalledTimes(2);
    expect(secondSignal?.aborted).toBe(false);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(status).toHaveBeenCalledTimes(2);
    expect(secondSignal?.aborted).toBe(false);

    await act(async () => {
      secondStatus.resolve(job({
        id: secondJobId,
        executionState: "canceled",
        canCancel: false,
        pollAfterSeconds: 0,
      }));
      await Promise.resolve();
    });

    expect(result.current.state).toMatchObject({
      phase: "terminal",
      job: { id: secondJobId, executionState: "canceled" },
    });
  });

  it("announces ready TTL only when a quiet threshold is crossed", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const api: BackupAssetExportApi = {
      create: vi.fn(), status: vi.fn(), cancel: vi.fn(), issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    const expiresAt = new Date(Date.now() + 61 * 60 * 1_000).toISOString();
    act(() => result.current.hydrate(readyJobWithExpiry(expiresAt)));
    expect(result.current.state.announcement).toBe("state:ready");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60 * 1_000);
    });
    expect(result.current.state.announcement).toBe("ttl_1h");
  });

  it("announces expiry after the final one-minute threshold", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const api: BackupAssetExportApi = {
      create: vi.fn(), status: vi.fn(), cancel: vi.fn(), issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(readyJobWithExpiry(new Date(Date.now() + 61_000).toISOString())));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(result.current.state.announcement).toBe("ttl_1m");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(result.current.state.announcement).toBe("ttl_expired");
  });

  it("locally closes ready download authority and waits for authoritative expiry reconciliation", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const statusResponse = deferred<BackupExportJob>();
    const api: BackupAssetExportApi = {
      create: vi.fn(), status: vi.fn().mockReturnValue(statusResponse.promise), cancel: vi.fn(), issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(readyJobWithExpiry(new Date(Date.now() + 1_000).toISOString())));
    expect(result.current.state.job).toMatchObject({ executionState: "ready", canDownload: true });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.state.announcement).toBe("ttl_1m");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(result.current.state.job).toMatchObject({
      executionState: "ready",
      canCancel: false,
      canDownload: false,
    });
    expect(result.current.state.announcement).toBe("ttl_expired");
    expect(api.status).toHaveBeenCalledWith("token", jobId, expect.objectContaining({ limit: 100 }));

    statusResponse.resolve(readyJob({
      executionState: "expiring",
      canCancel: false,
      canDownload: false,
      pollAfterSeconds: 1,
    }));
    await act(async () => {
      await Promise.resolve();
    });

    expect(result.current.state.job).toMatchObject({
      executionState: "expiring",
      canDownload: false,
    });
  });

  it("passes an explicitly selected tar compression profile through the typed create boundary", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: job(), replay: false }),
      status: vi.fn(), cancel: vi.fn(), issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create({ archiveFormat: "tar", archiveProfile: "tar_gzip_v1" }));

    expect(vi.mocked(api.create).mock.calls[0]?.[1]).toMatchObject({
      archiveFormat: "tar",
      archiveProfile: "tar_gzip_v1",
    });
  });

  it("rejects missing archive options before proof or create", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(), status: vi.fn(), cancel: vi.fn(), issueDownloadTicket: vi.fn(),
    };
    const options = baseOptions(api);
    const { result } = renderHook(() => useBackupAssetExport(options));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    const createWithoutOptions = result.current.create as (
      createOptions?: BackupAssetExportCreateOptions,
    ) => Promise<void>;

    await act(async () => createWithoutOptions(undefined));

    expect(result.current.state.error).toBe("invalid");
    expect(options.ensureStepUpProof).not.toHaveBeenCalled();
    expect(api.create).not.toHaveBeenCalled();
  });

  it("retains a bounded rolling window through ordinal 249 without losing continuation", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const firstPage = pagedReadyJob(0, 100, "page-two");
    const secondPage = pagedReadyJob(100, 100, "page-three");
    const thirdPage = pagedReadyJob(200, 50, null);
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn()
        .mockResolvedValueOnce(secondPage)
        .mockResolvedValueOnce(thirdPage),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(firstPage));

    await act(async () => result.current.loadMoreItems());

    expect(api.status).toHaveBeenCalledWith("token", jobId, {
      cursor: "page-two",
      limit: 100,
      signal: expect.any(AbortSignal),
    });
    expect(result.current.state.job?.items).toHaveLength(200);
    expect(result.current.state.job?.items.at(-1)?.ordinal).toBe(199);
    expect(result.current.state.job?.nextCursor).toBe("page-three");

    await act(async () => result.current.loadMoreItems());

    expect(api.status).toHaveBeenLastCalledWith("token", jobId, {
      cursor: "page-three",
      limit: 100,
      signal: expect.any(AbortSignal),
    });
    expect(result.current.state.job?.items).toHaveLength(200);
    expect(result.current.state.job?.items[0]?.ordinal).toBe(50);
    expect(result.current.state.job?.items.at(-1)?.ordinal).toBe(249);
    expect(result.current.state.job?.nextCursor).toBeNull();
  });

  it("loads a cursor page for an active export and retains at most 200 items", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const firstPage = pagedActiveJob(0, 100, "page-two");
    const secondPage = pagedActiveJob(100, 100, "page-three");
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue(secondPage),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(firstPage));

    await act(async () => result.current.loadMoreItems());

    expect(api.status).toHaveBeenCalledWith("token", jobId, {
      cursor: "page-two",
      limit: 100,
      signal: expect.any(AbortSignal),
    });
    expect(result.current.state.job?.items).toHaveLength(200);
    expect(result.current.state.job?.items.at(-1)?.ordinal).toBe(199);
    expect(result.current.state.job?.nextCursor).toBe("page-three");
  });

  it("accepts a same-job item page after progress counters advance", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const firstPage = { ...pagedActiveJob(0, 100, "page-two"), logicalBytes: 0 };
    const advancedPage = {
      ...pagedActiveJob(100, 100, "page-three"),
      packedCount: 1,
      logicalBytes: 1,
      providerBytes: 1,
    };
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue(advancedPage),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(firstPage));

    await act(async () => result.current.loadMoreItems());

    expect(result.current.state.error).toBeNull();
    expect(result.current.state.job).toMatchObject({
      packedCount: 1,
      logicalBytes: 1,
      providerBytes: 1,
      nextCursor: "page-three",
    });
    expect(result.current.state.job?.items).toHaveLength(200);
    expect(result.current.state.job?.items.at(-1)?.ordinal).toBe(199);
  });

  it("continues polling after loading an item page during an in-flight poll", async () => {
    vi.useFakeTimers();
    const firstPage = pagedActiveJob(0, 100, "page-two");
    const secondPage = pagedActiveJob(100, 100, "page-three");
    const polledStatus = deferred<BackupExportJob>();
    let pollCalls = 0;
    let pollSignal: AbortSignal | undefined;
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn((_token, _id, options: { cursor?: string; signal?: AbortSignal }) => {
        if (options.cursor) return Promise.resolve(secondPage);
        pollCalls += 1;
        if (pollCalls === 1) {
          pollSignal = options.signal;
          return polledStatus.promise;
        }
        return Promise.resolve(job({ executionState: "canceled", canCancel: false, pollAfterSeconds: 0 }));
      }),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.hydrate(firstPage));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(pollCalls).toBe(1);

    await act(async () => result.current.loadMoreItems());
    expect(pollSignal?.aborted).toBe(false);

    await act(async () => {
      polledStatus.resolve(firstPage);
      await Promise.resolve();
    });
    expect(result.current.state.job?.items).toHaveLength(200);
    expect(result.current.state.job?.items.at(-1)?.ordinal).toBe(199);
    expect(result.current.state.job?.nextCursor).toBe("page-three");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(pollCalls).toBe(2);
  });

  it("replace-clears the job route when loading another item page is forbidden", async () => {
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn().mockRejectedValue(new ApiError(403, "forbidden")),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api, onRouteChange)));
    act(() => result.current.hydrate(pagedActiveJob(0, 100, "page-two")));

    await act(async () => result.current.loadMoreItems());

    expect(result.current.state).toMatchObject({ phase: "error", error: "forbidden", job: null, ticket: null });
    expect(onRouteChange).toHaveBeenCalledWith(null, { replace: true });
  });

  it("ignores a duplicate create while preserving the cloned selection of the first request", async () => {
    let resolveProof: ((proof: string) => void) | null = null;
    const proof = new Promise<string>((resolve) => {
      resolveProof = resolve;
    });
    const api: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: job(), replay: false }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const mutableRef = { ...ref };
    const options = {
      ...baseOptions(api),
      ensureStepUpProof: vi.fn().mockReturnValue(proof),
    };
    const { result } = renderHook(() => useBackupAssetExport(options));
    act(() => result.current.open([mutableRef], { count: 1, logicalBytes: 77 }));

    let firstCreate: Promise<void> | undefined;
    act(() => {
      firstCreate = result.current.create(zipCreateOptions);
    });
    mutableRef.entryId = "9".repeat(64);
    await act(async () => result.current.create(zipCreateOptions));

    await act(async () => {
      resolveProof?.("fresh-proof");
      await firstCreate;
    });

    expect(options.ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(api.create).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.create).mock.calls[0]?.[1].selection).toEqual({
      schemaVersion: 1,
      kind: "explicit",
      refs: [ref],
    });
    expect(result.current.state.job?.id).toBe(jobId);
  });

  it("pauses polling while offline and reconciles immediately on the online event", async () => {
    vi.useFakeTimers();
    Object.defineProperty(navigator, "onLine", { configurable: true, value: false });
    const api: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: job(), replay: false }),
      status: vi.fn().mockResolvedValue(readyJob()),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));

    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(api.status).not.toHaveBeenCalled();

    Object.defineProperty(navigator, "onLine", { configurable: true, value: true });
    await act(async () => {
      window.dispatchEvent(new Event("online"));
      await Promise.resolve();
    });

    expect(api.status).toHaveBeenCalledWith("token", jobId, expect.objectContaining({ limit: 100 }));
    expect(result.current.state.job?.executionState).toBe("ready");
  });

  it("pauses polling while hidden and reconciles on visibility restore", async () => {
    vi.useFakeTimers();
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    const api: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: job(), replay: false }),
      status: vi.fn().mockResolvedValue(readyJob()),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api)));
    act(() => result.current.open([ref], { count: 1, logicalBytes: 77 }));
    await act(async () => result.current.create(zipCreateOptions));

    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(api.status).not.toHaveBeenCalled();

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await Promise.resolve();
    });

    expect(api.status).toHaveBeenCalledWith("token", jobId, expect.objectContaining({ limit: 100 }));
    expect(result.current.state.job?.executionState).toBe("ready");
  });

  it("does not resurrect a canceled job response after the panel is dismissed", async () => {
    let resolveCancel: ((value: BackupExportJob) => void) | null = null;
    const cancelResponse = new Promise<BackupExportJob>((resolve) => {
      resolveCancel = resolve;
    });
    const api: BackupAssetExportApi = {
      create: vi.fn(),
      status: vi.fn(),
      cancel: vi.fn().mockReturnValue(cancelResponse),
      issueDownloadTicket: vi.fn(),
    };
    const onRouteChange = vi.fn();
    const { result } = renderHook(() => useBackupAssetExport(baseOptions(api, onRouteChange)));
    act(() => result.current.hydrate(job()));

    let cancelPromise: Promise<void> | undefined;
    act(() => {
      cancelPromise = result.current.cancel();
    });
    act(() => result.current.dismiss());
    await act(async () => {
      resolveCancel?.(job({ executionState: "canceled", canCancel: false }));
      await cancelPromise;
    });

    expect(result.current.state.phase).toBe("closed");
    expect(result.current.state.job).toBeNull();
    expect(onRouteChange).toHaveBeenLastCalledWith(null, { replace: true });
  });
});

function readyJobWithExpiry(expiresAt: string): BackupExportJob {
  return readyJobWithTimes(new Date(Date.parse(expiresAt) - 1_000).toISOString(), expiresAt);
}

function readyJobWithTimes(readyAt: string, expiresAt: string): BackupExportJob {
  return job({
    executionState: "ready",
    resultKind: "complete",
    packedCount: 1,
    logicalBytes: 1234,
    providerBytes: 700,
    artifactBytes: 812,
    readyAt,
    expiresAt,
    canCancel: false,
    canDownload: true,
    pollAfterSeconds: 0,
    items: [{ id: "5".repeat(32), ordinal: 0, state: "packed", logicalBytes: 1234, providerBytes: 700, errorCategory: null }],
  });
}

function pagedReadyJob(start: number, count: number, nextCursor: string | null): BackupExportJob {
  const expiresAt = new Date(Date.now() + 60_000).toISOString();
  return job({
    executionState: "ready",
    resultKind: "complete",
    itemCount: 250,
    packedCount: 250,
    logicalBytes: 250,
    providerBytes: 250,
    artifactBytes: 512,
    readyAt: new Date(Date.now() - 1_000).toISOString(),
    expiresAt,
    canCancel: false,
    canDownload: true,
    pollAfterSeconds: 0,
    items: Array.from({ length: count }, (_, offset) => ({
      id: (start + offset + 1).toString(16).padStart(32, "0"),
      ordinal: start + offset,
      state: "packed" as const,
      logicalBytes: 1,
      providerBytes: 1,
      errorCategory: null,
    })),
    nextCursor,
  });
}

function pagedActiveJob(start: number, count: number, nextCursor: string | null): BackupExportJob {
  return job({
    itemCount: 250,
    logicalBytes: 250,
    items: Array.from({ length: count }, (_, offset) => ({
      id: (start + offset + 1).toString(16).padStart(32, "0"),
      ordinal: start + offset,
      state: "pending" as const,
      logicalBytes: 0,
      providerBytes: 0,
      errorCategory: null,
    })),
    nextCursor,
  });
}
