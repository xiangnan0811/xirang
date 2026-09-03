import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { CatalogGeneration, CatalogStatus } from "@/types/domain";
import {
  catalogReadinessTerminalNotice,
  isRecoveryPointCatalogReady,
  waitForRecoveryPointCatalogReady,
} from "./recovery-point-catalog-readiness";

const { getRecoveryPointCatalogStatus } = vi.hoisted(() => ({
  getRecoveryPointCatalogStatus: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: { getRecoveryPointCatalogStatus },
}));

const pointId = "2".repeat(32);

function catalogStatus(overrides: Partial<CatalogStatus> = {}): CatalogStatus {
  return {
    generation: {
      id: "3".repeat(32),
      sequence: 1,
      state: "complete",
      startedAt: "2026-08-29T00:00:00.000Z",
      finishedAt: "2026-08-29T00:00:01.000Z",
      errorCode: "",
      correlationId: "",
    },
    latestBuild: null,
    coverage: {
      status: "complete",
      indexedEntries: 0,
      expectedEntries: 0,
      manifestDigest: "",
      observedAt: "2026-08-29T00:00:01.000Z",
    },
    staleness: { status: "fresh", observedAt: null, reason: null },
    contentAvailability: { available: true, reason: null },
    permissions: { list: true, preview: false, download: false },
    ...overrides,
  };
}

function available(status: CatalogStatus = catalogStatus()): { status: "available"; value: CatalogStatus } {
  return { status: "available", value: status };
}
describe("waitForRecoveryPointCatalogReady", () => {
  beforeEach(() => {
    getRecoveryPointCatalogStatus.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns ready for an exact complete catalog", async () => {
    getRecoveryPointCatalogStatus.mockResolvedValue(available());
    const controller = new AbortController();

    await expect(waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: controller.signal,
    })).resolves.toEqual({ status: "ready", catalog: catalogStatus() });
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledWith("token", pointId, expect.any(AbortSignal));
  });

  it("does not treat a complete-coverage catalog as ready without a complete generation", () => {
    const status = catalogStatus({ generation: null });
    expect(isRecoveryPointCatalogReady(status)).toBe(false);
    expect(catalogReadinessTerminalNotice(status)).toBeNull();
  });

  it("does not treat a stale complete generation as ready while latestBuild is building", () => {
    const status = catalogStatus({
      staleness: { status: "stale", observedAt: "2026-08-29T00:01:00.000Z", reason: null },
      latestBuild: catalogGeneration("building", 2),
    });
    expect(isRecoveryPointCatalogReady(status)).toBe(false);
    expect(catalogReadinessTerminalNotice(status)).toBeNull();
  });

  it.each(["failed", "partial"] as const)(
    "treats a stale complete generation with latestBuild %s as terminal",
    (state) => {
      const status = catalogStatus({
        staleness: { status: "stale", observedAt: "2026-08-29T00:01:00.000Z", reason: null },
        latestBuild: catalogGeneration(state, 2),
      });
      expect(isRecoveryPointCatalogReady(status)).toBe(false);
      expect(catalogReadinessTerminalNotice(status)).toBe(state === "failed" ? "catalogFailed" : "catalogPartial");
    },
  );

  it("fails closed for a blocked catalog projection", async () => {
    getRecoveryPointCatalogStatus.mockResolvedValue({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });

    await expect(waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: new AbortController().signal,
    })).resolves.toEqual({ status: "blocked" });
  });

  it.each([
    ["failed", "catalogFailed"],
    ["partial", "catalogPartial"],
    ["unavailable", "catalogUnavailable"],
  ] as const)("returns a terminal %s notice without looping", async (coverage, notice) => {
    getRecoveryPointCatalogStatus.mockResolvedValue(available(catalogStatus({
      generation: coverage === "failed" || coverage === "partial"
        ? { ...catalogStatus().generation!, state: coverage }
        : catalogStatus().generation,
      coverage: { ...catalogStatus().coverage, status: coverage },
    })));

    await expect(waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: new AbortController().signal,
    })).resolves.toMatchObject({ status: "terminal", notice });
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(1);
  });

  it("polls a stale complete catalog until latestBuild becomes fresh", async () => {
    vi.useFakeTimers();
    const staleBuilding = catalogStatus({
      staleness: { status: "stale", observedAt: "2026-08-29T00:01:00.000Z", reason: null },
      latestBuild: catalogGeneration("building", 2),
    });
    getRecoveryPointCatalogStatus
      .mockResolvedValueOnce(available(staleBuilding))
      .mockResolvedValueOnce(available());

    const pending = waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: new AbortController().signal,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2_000);
    await expect(pending).resolves.toEqual({ status: "ready", catalog: catalogStatus() });
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(2);
  });

  it("returns a terminal notice for a stale catalog whose latestBuild failed", async () => {
    const staleFailed = catalogStatus({
      staleness: { status: "stale", observedAt: "2026-08-29T00:01:00.000Z", reason: null },
      latestBuild: catalogGeneration("failed", 2),
    });
    getRecoveryPointCatalogStatus.mockResolvedValue(available(staleFailed));

    await expect(waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: new AbortController().signal,
    })).resolves.toMatchObject({ status: "terminal", notice: "catalogFailed" });
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(1);
  });

  it("times out after bounded building polls", async () => {
    vi.useFakeTimers();
    getRecoveryPointCatalogStatus.mockResolvedValue(available(catalogStatus({
      generation: null,
      coverage: { ...catalogStatus().coverage, status: "building" },
      contentAvailability: { available: false, reason: null },
    })));

    const pending = waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: new AbortController().signal,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(118_000);
    await expect(pending).resolves.toEqual({ status: "timeout" });
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(60);
  });

  it("uses a wall-clock deadline when a catalog request never settles", async () => {
    vi.useFakeTimers();
    let catalogSignal: AbortSignal | undefined;
    getRecoveryPointCatalogStatus.mockImplementation((...args: unknown[]) => {
      catalogSignal = args[2] as AbortSignal;
      return new Promise(() => undefined);
    });

    const pending = waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: new AbortController().signal,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(119_999);
    expect(catalogSignal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await expect(pending).resolves.toEqual({ status: "timeout" });
    expect(catalogSignal?.aborted).toBe(true);
  });

  it("aborts an in-flight catalog request and ignores a late result", async () => {
    vi.useFakeTimers();
    let catalogSignal: AbortSignal | undefined;
    let resolveCatalog!: (value: { status: "available"; value: CatalogStatus }) => void;
    getRecoveryPointCatalogStatus.mockImplementation((...args: unknown[]) => {
      catalogSignal = args[2] as AbortSignal;
      return new Promise<{ status: "available"; value: CatalogStatus }>((resolve) => {
        resolveCatalog = resolve;
      });
    });
    const controller = new AbortController();
    const pending = waitForRecoveryPointCatalogReady({
      token: "token",
      recoveryPointId: pointId,
      signal: controller.signal,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(catalogSignal?.aborted).toBe(false);
    expect(vi.getTimerCount()).toBe(1);

    controller.abort();
    await expect(pending).resolves.toEqual({ status: "aborted" });
    expect(catalogSignal?.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);

    resolveCatalog(available());
    await vi.advanceTimersByTimeAsync(0);
  });
});

function catalogGeneration(state: CatalogGeneration["state"], sequence: number): CatalogGeneration {
  return {
    id: String(sequence).repeat(32),
    sequence,
    state,
    startedAt: "2026-08-29T00:01:00.000Z",
    finishedAt: state === "building" ? null : "2026-08-29T00:01:01.000Z",
    errorCode: state === "failed" ? "catalog_build_failed" : "",
    correlationId: "",
  };
}
