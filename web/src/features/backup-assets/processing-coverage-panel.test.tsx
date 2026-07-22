import "@testing-library/jest-dom/vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { runAxe } from "@/test/a11y-helpers";
import type {
  BackupProcessingAdminControl,
  BackupProcessingBackfillPolicy,
  BackupProcessingCoverageSummary,
  BackupProcessingUpdaterCandidate,
  BackupProcessingUpdaterStatus,
} from "@/types/domain";

import {
  ProcessingCoveragePanel,
  type BackupProcessingAdminClient,
} from "./processing-coverage-panel";

const revision = "1".repeat(64);
const candidateId = "2".repeat(32);

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function control(): BackupProcessingAdminControl {
  return {
    schemaVersion: 1,
    configured: true,
    localEnabled: true,
    remoteEnabled: false,
    backfillPolicy: {
      schemaVersion: 1,
      revision,
      paused: true,
      batchSize: 50,
      jobsPerHour: 500,
      bytesPerHour: 1_073_741_824,
      providerConcurrency: 2,
      capabilityConcurrency: 2,
    },
  };
}

function coverage(): BackupProcessingCoverageSummary {
  return {
    schemaVersion: 1,
    generatedAt: new Date(Date.now() - 60_000).toISOString(),
    eligible: 12,
    completed: 8,
    partial: 1,
    queued: 2,
    failed: 1,
    unsupported: 0,
    notDeployed: 0,
    stale: 1,
    backlogAgeBucket: "under_1h",
    estimatedSeconds: 60,
    byCapability: [{
      capability: "image.thumbnail",
      profile: "raster_thumbnail_v1",
      eligible: 12,
      completed: 8,
      partial: 1,
      queued: 2,
      failed: 1,
      unsupported: 0,
      notDeployed: 0,
      stale: 1,
    }],
  };
}

function candidate(): BackupProcessingUpdaterCandidate {
  return {
    candidateId,
    sourceKind: "admin_registered",
    sourceId: "offline-2026-07",
    version: "1.2.3",
    manifestDigest: "3".repeat(64),
    signingKeyFingerprint: "4".repeat(64),
    bundleFingerprint: "5".repeat(64),
    state: "verified",
    reason: null,
    verifiedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
    activatedAt: null,
    capabilityChanges: [{
      capability: "image.thumbnail",
      capabilitySchema: "image.thumbnail.v1",
      profiles: ["raster_thumbnail_v1"],
    }],
  };
}

function updater(): BackupProcessingUpdaterStatus {
  return { schemaVersion: 1, enabled: true, onlineEnabled: false, active: null };
}

function client(): BackupProcessingAdminClient {
  return {
    getAdminControl: vi.fn(async () => control()),
    getCoverage: vi.fn(async () => coverage()),
    getUpdaterStatus: vi.fn(async () => updater()),
    listOfflineCandidates: vi.fn(async () => [candidate()]),
    scanOfflineCandidates: vi.fn(async () => undefined),
    activateOfflineCandidate: vi.fn(async () => undefined),
    updateBackfillPolicy: vi.fn(async (_token, policy) => ({ ...policy, revision: "6".repeat(64) })),
  };
}

function renderPanel(api: BackupProcessingAdminClient, role: "admin" | "operator" = "admin") {
  const props = { token: "token", role, loadApi: async () => api };
  return render(<ProcessingCoveragePanel {...props} />);
}

describe("ProcessingCoveragePanel", () => {
  let api: BackupProcessingAdminClient;

  beforeEach(() => {
    api = client();
  });

  it("renders bounded coverage and verified candidate metadata without import transports", async () => {
    const { container } = renderPanel(api);

    expect(await screen.findByText(/8 of 12|12 项中已完成 8 项/)).toBeInTheDocument();
    expect(screen.getByText("1.2.3")).toBeInTheDocument();
    expect(screen.getAllByText("image.thumbnail")).toHaveLength(2);
    expect(document.querySelector('input[type="file"]')).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/URL|Path|路径|地址/)).not.toBeInTheDocument();
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("uses JSON-only scan and activation controls with the current active fingerprint", async () => {
    const user = userEvent.setup();
    const active = { ...candidate(), state: "active" as const, bundleFingerprint: "7".repeat(64) };
    vi.mocked(api.getUpdaterStatus).mockResolvedValue({ ...updater(), active });
    renderPanel(api);

    await user.click(await screen.findByRole("button", { name: /Scan inbox|扫描收件箱/ }));
    await waitFor(() => expect(api.scanOfflineCandidates).toHaveBeenCalledWith("token", expect.any(AbortSignal)));
    await user.click(screen.getByRole("button", { name: /Activate 1.2.3|激活 1.2.3/ }));
    await waitFor(() => expect(api.activateOfflineCandidate).toHaveBeenCalledWith(
      "token",
      candidateId,
      "7".repeat(64),
      expect.any(AbortSignal)
    ));
  });

  it("updates the paused policy through the current revision", async () => {
    const user = userEvent.setup();
    renderPanel(api);

    const pause = await screen.findByRole("switch", { name: /Pause background processing|暂停后台处理/ });
    expect(pause).toBeChecked();
    await user.click(pause);
    await user.click(screen.getByRole("button", { name: /Save limits|保存限额/ }));
    await waitFor(() => expect(api.updateBackfillPolicy).toHaveBeenCalledWith(
      "token",
      expect.objectContaining({ revision, paused: false, jobsPerHour: 500 }),
      expect.any(AbortSignal)
    ));
  });

  it("isolates a deferred mutation from a new token and role generation", async () => {
    const user = userEvent.setup();
    const oldMutation = deferred<BackupProcessingBackfillPolicy>();
    let oldMutationSignal: AbortSignal | undefined;
    vi.mocked(api.updateBackfillPolicy).mockImplementation((_token, _policy, signal) => {
      oldMutationSignal = signal;
      return oldMutation.promise;
    });
    const nextApi = client();
    const nextMutation = deferred<BackupProcessingBackfillPolicy>();
    vi.mocked(nextApi.updateBackfillPolicy).mockReturnValue(nextMutation.promise);
    const nextRevision = "8".repeat(64);
    vi.mocked(nextApi.getAdminControl).mockResolvedValue({
      ...control(),
      backfillPolicy: {
        ...control().backfillPolicy,
        revision: nextRevision,
        paused: false,
        jobsPerHour: 900,
      },
    });
    const firstProps = { token: "token-a", role: "admin" as const, loadApi: async () => api };
    const operatorProps = { token: "token-b", role: "operator" as const, loadApi: async () => nextApi };
    const nextProps = { token: "token-b", role: "admin" as const, loadApi: async () => nextApi };
    const { rerender } = render(<ProcessingCoveragePanel {...firstProps} />);

    const pause = await screen.findByRole("switch", { name: /Pause background processing|暂停后台处理/ });
    await user.click(pause);
    await user.click(screen.getByRole("button", { name: /Save limits|保存限额/ }));
    await waitFor(() => expect(api.updateBackfillPolicy).toHaveBeenCalledTimes(1));
    expect(oldMutationSignal?.aborted).toBe(false);

    rerender(<ProcessingCoveragePanel {...operatorProps} />);
    expect(oldMutationSignal?.aborted).toBe(true);
    rerender(<ProcessingCoveragePanel {...nextProps} />);

    const jobsPerHour = await screen.findByLabelText(/Jobs per hour|每小时任务数/);
    expect(jobsPerHour).toHaveValue(900);
    expect(screen.getByRole("switch", { name: /Pause background processing|暂停后台处理/ })).toBeEnabled();
    const nextSave = screen.getByRole("button", { name: /Save limits|保存限额/ });
    expect(nextSave).toBeEnabled();
    await user.click(nextSave);
    await waitFor(() => expect(nextApi.updateBackfillPolicy).toHaveBeenCalledTimes(1));
    expect(nextSave).toBeDisabled();

    await act(async () => {
      oldMutation.resolve({
        ...control().backfillPolicy,
        revision: "9".repeat(64),
        paused: true,
        jobsPerHour: 111,
      });
      await oldMutation.promise;
    });

    expect(jobsPerHour).toHaveValue(900);
    expect(screen.getByRole("switch", { name: /Pause background processing|暂停后台处理/ })).not.toBeChecked();
    expect(nextSave).toBeDisabled();

    await act(async () => {
      nextMutation.resolve({
        ...control().backfillPolicy,
        revision: "a".repeat(64),
        paused: false,
        jobsPerHour: 901,
      });
      await nextMutation.promise;
    });
    expect(jobsPerHour).toHaveValue(901);
    expect(nextSave).toBeEnabled();
  });

  it("binds ready resource rendering to the current Admin token before effects run", () => {
    const source = readFileSync(
      path.resolve(process.cwd(), "src/features/backup-assets/processing-coverage-panel.tsx"),
      "utf8"
    );
    const tokenGuard = source.indexOf("scopeRef.current.token !== token");
    const readyResourceRead = source.indexOf("const { control, coverage, updater, candidates }");

    expect(tokenGuard).toBeGreaterThan(-1);
    expect(tokenGuard).toBeLessThan(readyResourceRead);
  });

  it("does not load Admin data for a non-Admin role", () => {
    const { container } = renderPanel(api, "operator");

    expect(container).toBeEmptyDOMElement();
    expect(api.getCoverage).not.toHaveBeenCalled();
  });
});
