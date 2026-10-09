import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier, formatTime, releaseAuthTransitionBarrier, rememberAuthIdentity } from "@/lib/api/core";
import type { CronBackupStatus, CronJobObservation } from "@/lib/api/cron-backup-status";
import type { BackupEntry, BackupResult } from "@/lib/api/system-api";
import { SelfBackupPanel } from "./self-backup-panel";

const { auth, listBackups, backupDB, getCronBackupStatus, toastSuccess, toastError } = vi.hoisted(() => ({
  auth: {
    token: "old-session" as string | null,
    role: "admin" as "admin" | "operator" | "viewer" | null,
  },
  listBackups: vi.fn(),
  backupDB: vi.fn(),
  getCronBackupStatus: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => auth }));
vi.mock("@/lib/api/client", () => ({ apiClient: { listBackups, backupDB, getCronBackupStatus } }));
vi.mock("sonner", () => ({ toast: { success: toastSuccess, error: toastError } }));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function idleJob(status: CronJobObservation["status"], checkedAt: string): CronJobObservation {
  return { evidence: "job_record", status, checkedAt, maxAgeSeconds: 93600 };
}

const notConfigured: CronBackupStatus = {
  status: "not_configured",
  engine: "sqlite",
  checkedAt: "2026-10-07T00:00:00Z",
  maxAgeSeconds: 93600,
  evidence: "artifact_pair",
  timeSource: "mtime",
  contentVerified: false,
  job: idleJob("not_configured", "2026-10-07T00:00:00Z"),
};

const freshPostgres: CronBackupStatus = {
  status: "fresh",
  engine: "postgres",
  checkedAt: "2026-10-07T02:00:00Z",
  maxAgeSeconds: 93600,
  directory: "/backup/db",
  latestCompleteAt: "2026-10-07T01:30:00Z",
  artifactName: "xirang-postgres-20261007-013000.dump",
  evidence: "artifact_pair",
  timeSource: "mtime",
  contentVerified: false,
  job: idleJob("not_configured", "2026-10-07T02:00:00Z"),
};

function jobRegion(): HTMLElement {
  const region = document.querySelector('[data-evidence="job_record"]');
  if (!(region instanceof HTMLElement)) throw new Error("job record region is missing");
  return region;
}

function artifactRegion(): HTMLElement {
  const region = document.querySelector('[data-evidence="artifact_pair"]');
  if (!(region instanceof HTMLElement)) throw new Error("artifact region is missing");
  return region;
}

beforeEach(() => {
  clearAuthTransitionBarrier();
  auth.token = "old-session";
  auth.role = "admin";
  rememberAuthIdentity(auth.token, auth.role);
  listBackups.mockReset();
  backupDB.mockReset();
  getCronBackupStatus.mockReset();
  toastSuccess.mockReset();
  toastError.mockReset();
  listBackups.mockResolvedValue([]);
  getCronBackupStatus.mockResolvedValue(notConfigured);
});

afterEach(() => {
  clearAuthTransitionBarrier();
  rememberAuthIdentity(null, null);
});

it("aborts an old session list and does not show its late response in the new session", async () => {
  let resolveOld!: (value: BackupEntry[]) => void;
  const oldRequest = new Promise<BackupEntry[]>((resolve) => { resolveOld = resolve; });
  listBackups.mockReset();
  listBackups.mockReturnValueOnce(oldRequest).mockResolvedValueOnce([
    { filename: "current.db", size: 1, createdAt: "2026-09-17T00:00:00Z", sha256: "test" },
  ]);
  const { rerender } = render(<SelfBackupPanel />);
  const oldSignal: AbortSignal = listBackups.mock.calls[0][1].signal;
  auth.token = "new-session";
  rememberAuthIdentity(auth.token, "admin");
  rerender(<SelfBackupPanel />);
  expect(oldSignal.aborted).toBe(true);
  expect(await screen.findByText("current.db")).toBeInTheDocument();
  await act(async () => {
    resolveOld([{ filename: "stale.db", size: 1, createdAt: "2026-09-17T00:00:00Z", sha256: "test" }]);
  });
  expect(screen.queryByText("stale.db")).not.toBeInTheDocument();
});

it("does not toast or retry a backup after the auth generation changes", async () => {
  const pending = deferred<BackupResult>();
  backupDB.mockReturnValueOnce(pending.promise);
  render(<SelfBackupPanel />);
  fireEvent.click(screen.getByRole("button", { name: "立即备份" }));
  await waitFor(() => expect(backupDB).toHaveBeenCalledTimes(1));
  bumpAuthSessionGeneration();
  await act(async () => {
    pending.resolve({ filename: "late.db", path: "/late.db", size: 8, sha256: "abc" });
  });
  expect(toastSuccess).not.toHaveBeenCalled();
  expect(toastError).not.toHaveBeenCalled();
  expect(backupDB).toHaveBeenCalledTimes(1);
  expect(screen.queryByText("late.db")).not.toBeInTheDocument();
});

it("aborts an in-flight backup when admin identity leaves and returns", async () => {
  const pending = deferred<BackupResult>();
  backupDB.mockReturnValueOnce(pending.promise);
  const { rerender } = render(<SelfBackupPanel />);
  fireEvent.click(screen.getByRole("button", { name: "立即备份" }));
  await waitFor(() => expect(backupDB).toHaveBeenCalledTimes(1));
  const signal: AbortSignal = backupDB.mock.calls[0][1].signal;
  auth.role = "operator";
  rememberAuthIdentity(auth.token, "operator");
  bumpAuthSessionGeneration();
  rerender(<SelfBackupPanel />);
  expect(signal.aborted).toBe(true);
  auth.role = "admin";
  rememberAuthIdentity(auth.token, "admin");
  bumpAuthSessionGeneration();
  rerender(<SelfBackupPanel />);
  await act(async () => {
    pending.resolve({ filename: "stale.db", path: "/stale.db", size: 8, sha256: "abc" });
  });
  expect(toastSuccess).not.toHaveBeenCalled();
  expect(backupDB).toHaveBeenCalledTimes(1);
  expect(screen.queryByText("stale.db")).not.toBeInTheDocument();
});

it("describes the sqlite snapshot limits and shows observed cron evidence without inventing a run", async () => {
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.noRecords"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.desc"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.snapshotCaveat"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.retentionNote"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.cronDisclaimer"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.cronNotEvidence"))).toBeInTheDocument();
  expect(within(artifactRegion()).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.status.not_configured"));
  expect(within(jobRegion()).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.jobStatus.not_configured"));
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobAttempt"))).not.toBeInTheDocument();
  expect(within(jobRegion()).queryByRole("button")).not.toBeInTheDocument();
  expect(screen.getAllByText(i18n.t("selfBackup.thresholdHours", { hours: 26 }))).toHaveLength(2);
  expect(screen.getByText(i18n.t("selfBackup.contentNotVerified"))).toBeInTheDocument();
  expect(screen.getAllByText(notConfigured.checkedAt)).toHaveLength(2);
  expect(screen.getAllByText(formatTime(notConfigured.checkedAt)).length).toBeGreaterThan(0);
  const observer = document.querySelector('[data-panel="cron-backup-status"]');
  expect(observer?.tagName).toBe("DIV");
  expect(observer?.querySelector("h3")?.id).toBe("cron-backup-evidence-heading");
  expect(observer?.querySelector("button")).toBeNull();
  expect(screen.queryByText(i18n.t("selfBackup.cronDirectory"))).not.toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.cronLatestComplete"))).not.toBeInTheDocument();
  expect(screen.getByRole("link", { name: i18n.t("selfBackup.offlineRecovery") })).toHaveAttribute(
    "href",
    "https://github.com/xiangnan0811/xirang/blob/main/docs/deployment.md#手动备份与恢复",
  );
});

it("retries a failed web list without creating a backup", async () => {
  listBackups.mockReset();
  listBackups
    .mockRejectedValueOnce(new ApiError(503, "unavailable"))
    .mockResolvedValueOnce([{ filename: "after-retry.db", size: 4, createdAt: "2026-10-07T00:00:00Z", sha256: "abc" }]);
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.listLoadFailed"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.noRecords"))).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.retryList") }));
  expect(screen.getByText(i18n.t("selfBackup.loadingList"))).toBeInTheDocument();
  expect(await screen.findByText("after-retry.db")).toBeInTheDocument();
  expect(backupDB).not.toHaveBeenCalled();
});

it("keeps cron evidence visible when web self-backup is sqlite-only", async () => {
  listBackups.mockReset();
  listBackups.mockRejectedValue(new ApiError(501, "当前仅支持 SQLite 数据库备份"));
  getCronBackupStatus.mockReset();
  getCronBackupStatus.mockResolvedValue(freshPostgres);
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.sqliteOnly"))).toBeInTheDocument();
  const observer = document.querySelector('[data-panel="cron-backup-status"]');
  expect(observer).not.toBeNull();
  expect(observer).toHaveTextContent(freshPostgres.artifactName ?? "");
  expect(observer).toHaveTextContent("/backup/db");
  expect(observer).toHaveTextContent(freshPostgres.latestCompleteAt ?? "");
  expect(observer).toHaveTextContent(formatTime(freshPostgres.latestCompleteAt ?? ""));
  expect(within(artifactRegion()).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.status.fresh"));
  expect(within(artifactRegion()).getByRole("status").querySelector(".bg-success")).not.toBeNull();
  expect(within(jobRegion()).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.jobStatus.not_configured"));
  expect(within(jobRegion()).getByRole("status").querySelector(".bg-success")).toBeNull();
  expect(screen.getByText(i18n.t("selfBackup.engine.postgres"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.noRecords"))).not.toBeInTheDocument();
  expect(getCronBackupStatus).toHaveBeenCalled();
  const createButton = screen.getByRole("button", { name: "立即备份" });
  expect(createButton).toBeDisabled();
  fireEvent.click(createButton);
  expect(backupDB).not.toHaveBeenCalled();
});

it("shows cron evidence while the web list is still loading", async () => {
  const pending = deferred<BackupEntry[]>();
  listBackups.mockReset();
  listBackups.mockReturnValueOnce(pending.promise);
  getCronBackupStatus.mockReset();
  getCronBackupStatus.mockResolvedValue(freshPostgres);
  render(<SelfBackupPanel />);
  expect(await screen.findByText(freshPostgres.artifactName ?? "")).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.loadingList"))).toBeInTheDocument();
  await act(async () => {
    pending.reject(new ApiError(501, "当前仅支持 SQLite 数据库备份"));
  });
  expect(await screen.findByText(i18n.t("selfBackup.sqliteOnly"))).toBeInTheDocument();
  expect(screen.getByText(freshPostgres.artifactName ?? "")).toBeInTheDocument();
});

it("reports backup creation separately from a failed refresh", async () => {
  listBackups.mockReset();
  listBackups.mockResolvedValueOnce([]).mockRejectedValueOnce(new ApiError(500, "refresh failed"));
  backupDB.mockResolvedValueOnce({ filename: "created.db", path: "/created.db", size: 12, sha256: "abc" });
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.noRecords"))).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "立即备份" }));
  await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(
    i18n.t("selfBackup.backupSuccess", { filename: "created.db", size: "12 B" }),
  ));
  expect(await screen.findByText(i18n.t("selfBackup.listLoadFailed"))).toBeInTheDocument();
  expect(toastError).toHaveBeenCalledWith(i18n.t("selfBackup.refreshFailed"));
  expect(screen.queryByText(i18n.t("selfBackup.noRecords"))).not.toBeInTheDocument();
  expect(backupDB).toHaveBeenCalledTimes(1);
});

it("clears backup busy when an auth transition ends without replaying create", async () => {
  const pending = deferred<BackupResult>();
  backupDB.mockReturnValueOnce(pending.promise);
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.noRecords"))).toBeInTheDocument();
  const listCalls = listBackups.mock.calls.length;
  fireEvent.click(screen.getByRole("button", { name: "立即备份" }));
  await waitFor(() => expect(backupDB).toHaveBeenCalledTimes(1));
  expect(screen.getByRole("button", { name: "立即备份" })).toBeDisabled();
  const signal: AbortSignal = backupDB.mock.calls[0][1].signal;
  const transitionId = beginAuthTransitionBarrier();
  expect(signal.aborted).toBe(true);
  await act(async () => {
    releaseAuthTransitionBarrier(transitionId);
  });
  await waitFor(() => {
    expect(screen.getByRole("button", { name: "立即备份" })).toBeEnabled();
    expect(listBackups.mock.calls.length).toBeGreaterThan(listCalls);
  });
  expect(backupDB).toHaveBeenCalledTimes(1);
  await act(async () => {
    pending.resolve({ filename: "late.db", path: "/late.db", size: 8, sha256: "abc" });
  });
  expect(toastSuccess).not.toHaveBeenCalled();
  expect(toastError).not.toHaveBeenCalled();
  expect(backupDB).toHaveBeenCalledTimes(1);
  expect(screen.queryByText("late.db")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "立即备份" })).toBeEnabled();
});

it("shows sqlite-only unsupported state when create is rejected with 501", async () => {
  backupDB.mockRejectedValueOnce(new ApiError(501, "当前仅支持 SQLite 数据库备份"));
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.noRecords"))).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "立即备份" }));
  expect(await screen.findByText(i18n.t("selfBackup.sqliteOnly"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.noRecords"))).not.toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.status.not_configured"))).toBeInTheDocument();
  expect(toastSuccess).not.toHaveBeenCalled();
  expect(toastError).not.toHaveBeenCalled();
  expect(backupDB).toHaveBeenCalledTimes(1);
});

it("does not request web or cron backup data for a non-admin", async () => {
  auth.role = "viewer";
  rememberAuthIdentity(auth.token, "viewer");
  render(<SelfBackupPanel />);
  await act(async () => {
    await Promise.resolve();
  });
  expect(screen.queryByRole("heading", { name: i18n.t("selfBackup.title") })).not.toBeInTheDocument();
  expect(listBackups).not.toHaveBeenCalled();
  expect(getCronBackupStatus).not.toHaveBeenCalled();
  expect(backupDB).not.toHaveBeenCalled();
});

it("shows invalid configuration with an unknown engine instead of a fetch error", async () => {
  getCronBackupStatus.mockResolvedValue({
    status: "invalid_configuration",
    engine: "",
    checkedAt: "2026-10-07T02:00:00.123456789Z",
    maxAgeSeconds: 93600,
    evidence: "artifact_pair",
    timeSource: "mtime",
    contentVerified: false,
    job: idleJob("invalid_configuration", "2026-10-07T02:00:00.123456789Z"),
  });
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.status.invalid_configuration"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.jobStatus.invalid_configuration"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.engine.unknown"))).toBeInTheDocument();
  expect(screen.getAllByText("2026-10-07T02:00:00.123456789Z")).toHaveLength(2);
  expect(screen.queryByText(i18n.t("selfBackup.cronLoadError"))).not.toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.engine.sqlite"))).not.toBeInTheDocument();
});

it("shows an observer read failure without turning a malformed payload into a fresh backup", async () => {
  listBackups.mockResolvedValue([{ filename: "web.db", size: 4, createdAt: "2026-10-07T00:00:00Z", sha256: "abc" }]);
  getCronBackupStatus.mockReset();
  getCronBackupStatus
    .mockRejectedValueOnce(new Error("invalid cron backup status"))
    .mockResolvedValueOnce(freshPostgres);
  render(<SelfBackupPanel />);
  expect(await screen.findByText("web.db")).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.cronLoadError"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.status.fresh"))).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.retryCron") }));
  expect(await screen.findByText(freshPostgres.artifactName ?? "")).toBeInTheDocument();
  expect(backupDB).not.toHaveBeenCalled();
});

it("drops a late cron status from the previous session", async () => {
  let resolveOld!: (value: CronBackupStatus) => void;
  const oldRequest = new Promise<CronBackupStatus>((resolve) => {
    resolveOld = resolve;
  });
  getCronBackupStatus.mockReset();
  getCronBackupStatus.mockReturnValueOnce(oldRequest).mockResolvedValueOnce({
    ...freshPostgres,
    artifactName: "xirang-postgres-current.dump",
  });
  const { rerender } = render(<SelfBackupPanel />);
  const oldSignal: AbortSignal = getCronBackupStatus.mock.calls[0][1].signal;
  auth.token = "new-session";
  rememberAuthIdentity(auth.token, "admin");
  rerender(<SelfBackupPanel />);
  expect(oldSignal.aborted).toBe(true);
  expect(await screen.findByText("xirang-postgres-current.dump")).toBeInTheDocument();
  await act(async () => {
    resolveOld({ ...freshPostgres, artifactName: "xirang-postgres-stale.dump" });
  });
  expect(screen.queryByText("xirang-postgres-stale.dump")).not.toBeInTheDocument();
});

const OLD_SUCCESS = {
  runId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  startedAt: "2026-10-07T01:29:00Z",
  finishedAt: "2026-10-07T01:30:00Z",
  artifactName: "xirang-sqlite-20261007-013000.db",
};

function observedBackup(overrides: Partial<CronBackupStatus>): CronBackupStatus {
  return {
    ...notConfigured,
    status: "no_complete_backup",
    directory: "/backup/db",
    ...overrides,
  };
}

it("keeps a failed job and its older success beside a fresh artifact pair", async () => {
  getCronBackupStatus.mockResolvedValue(observedBackup({
    status: "fresh",
    checkedAt: "2026-10-08T02:00:00Z",
    latestCompleteAt: "2026-10-08T01:40:00Z",
    artifactName: "xirang-sqlite-20261008-014000.db",
    job: {
      evidence: "job_record",
      status: "failed",
      checkedAt: "2026-10-08T02:00:00Z",
      maxAgeSeconds: 93600,
      latestAttempt: {
        runId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        startedAt: "2026-10-08T01:50:00Z",
        result: "failed",
        finishedAt: "2026-10-08T01:51:00Z",
        failureCode: "backup_failed",
      },
      lastSuccess: OLD_SUCCESS,
    },
  }));
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.jobStatus.failed"))).toBeInTheDocument();
  const observer = document.querySelector('[data-panel="cron-backup-status"]');
  expect(observer?.className).not.toMatch(/bg-success/);
  expect(within(jobRegion()).getByRole("alert").querySelector(".bg-warning")).not.toBeNull();
  expect(within(jobRegion()).getByRole("alert")).toHaveTextContent(i18n.t("selfBackup.jobStatus.failed"));
  expect(within(jobRegion()).getByText(OLD_SUCCESS.artifactName)).toBeInTheDocument();
  expect(within(jobRegion()).getByText(i18n.t("selfBackup.failureCode.backup_failed"))).toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobStatus.success"))).not.toBeInTheDocument();
  expect(within(artifactRegion()).getByRole("status").querySelector(".bg-success")).not.toBeNull();
  expect(within(artifactRegion()).getByText("xirang-sqlite-20261008-014000.db")).toBeInTheDocument();
  expect(observer?.querySelector("button")).toBeNull();
  expect(backupDB).not.toHaveBeenCalled();
});

it("shows running, interrupted, and invalid job records without offering to run a backup", async () => {
  async function show(status: CronBackupStatus) {
    getCronBackupStatus.mockResolvedValue(status);
    const view = render(<SelfBackupPanel />);
    await screen.findByText(i18n.t(`selfBackup.jobStatus.${status.job.status}`));
    return view;
  }

  const running = await show(observedBackup({
    job: {
      evidence: "job_record",
      status: "running",
      checkedAt: "2026-10-08T02:00:00Z",
      maxAgeSeconds: 93600,
      latestAttempt: {
        runId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        startedAt: "2026-10-08T01:50:00Z",
        result: "running",
      },
    },
  }));
  expect(within(jobRegion()).getByRole("status").querySelector(".bg-info")).not.toBeNull();
  expect(within(jobRegion()).getByText("2026-10-08T01:50:00Z")).toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobFinishedAt"))).not.toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobDetectedAt"))).not.toBeInTheDocument();
  expect(document.querySelector('[data-panel="cron-backup-status"]')?.querySelector("button")).toBeNull();
  running.unmount();

  const derived = await show(observedBackup({
    job: {
      evidence: "job_record",
      status: "interrupted",
      checkedAt: "2026-10-08T04:00:00Z",
      maxAgeSeconds: 93600,
      latestAttempt: {
        runId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        startedAt: "2026-10-07T01:00:00Z",
        result: "running",
      },
      lastSuccess: OLD_SUCCESS,
    },
  }));
  expect(within(jobRegion()).getByRole("alert")).toHaveTextContent(i18n.t("selfBackup.jobDetail.interrupted"));
  expect(within(jobRegion()).getByText(i18n.t("selfBackup.attemptResult.running"))).toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.attemptResult.interrupted"))).not.toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobDetectedAt"))).not.toBeInTheDocument();
  expect(within(jobRegion()).getByText(OLD_SUCCESS.artifactName)).toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobStatus.success"))).not.toBeInTheDocument();
  derived.unmount();

  const stored = await show(observedBackup({
    job: {
      evidence: "job_record",
      status: "interrupted",
      checkedAt: "2026-10-08T02:00:00Z",
      maxAgeSeconds: 93600,
      latestAttempt: {
        runId: "cccccccccccccccccccccccccccccccc",
        startedAt: "2026-10-08T01:50:00Z",
        result: "interrupted",
        detectedAt: "2026-10-08T01:55:00Z",
        failureCode: "process_interrupted",
      },
    },
  }));
  expect(within(jobRegion()).getByText("2026-10-08T01:55:00Z")).toBeInTheDocument();
  expect(within(jobRegion()).getByText(i18n.t("selfBackup.failureCode.process_interrupted"))).toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobFinishedAt"))).not.toBeInTheDocument();
  stored.unmount();

  await show(observedBackup({ job: idleJob("state_invalid", "2026-10-08T02:00:00Z") }));
  expect(within(jobRegion()).getByRole("alert")).toHaveTextContent(i18n.t("selfBackup.jobDetail.state_invalid"));
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobAttempt"))).not.toBeInTheDocument();
  expect(within(artifactRegion()).getByText(i18n.t("selfBackup.contentNotVerified"))).toBeInTheDocument();
  expect(document.querySelector('[data-panel="cron-backup-status"]')?.querySelector("button")).toBeNull();
  expect(backupDB).not.toHaveBeenCalled();
});

it("shows a visible completion record when the artifact pair is gone", async () => {
  getCronBackupStatus.mockResolvedValue({
    status: "no_complete_backup",
    engine: "sqlite",
    checkedAt: "2026-10-07T02:00:00.123Z",
    maxAgeSeconds: 93600,
    directory: "/backup/db",
    evidence: "artifact_pair",
    timeSource: "mtime",
    contentVerified: false,
    job: {
      evidence: "job_record",
      status: "success",
      checkedAt: "2026-10-07T02:00:00.123Z",
      maxAgeSeconds: 93600,
      latestAttempt: { ...OLD_SUCCESS, result: "success" },
      lastSuccess: OLD_SUCCESS,
    },
  });
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.jobStatus.success"))).toBeInTheDocument();
  expect(within(jobRegion()).getByText(i18n.t("selfBackup.jobDisclaimer"))).toBeInTheDocument();
  expect(within(jobRegion()).getByRole("status").querySelector(".bg-success")).not.toBeNull();
  expect(within(jobRegion()).getAllByText(OLD_SUCCESS.artifactName)).toHaveLength(2);
  expect(within(artifactRegion()).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.status.no_complete_backup"));
  expect(within(artifactRegion()).getByRole("status").querySelector(".bg-success")).toBeNull();
  expect(within(artifactRegion()).queryByText(OLD_SUCCESS.artifactName)).not.toBeInTheDocument();
  expect(within(artifactRegion()).getByText(i18n.t("selfBackup.contentNotVerified"))).toBeInTheDocument();
  expect(document.querySelector('[data-panel="cron-backup-status"]')?.className).not.toMatch(/bg-success/);
  expect(document.querySelector('[data-panel="cron-backup-status"]')?.querySelector("button")).toBeNull();
  expect(backupDB).not.toHaveBeenCalled();
});

it("shows a lock probe failure and a clock anomaly without turning recorded times into success", async () => {
  getCronBackupStatus.mockResolvedValue(observedBackup({
    job: {
      evidence: "job_record",
      status: "state_unavailable",
      checkedAt: "2026-10-07T02:00:00Z",
      maxAgeSeconds: 93600,
      latestAttempt: {
        runId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        startedAt: "2026-10-08T00:00:00Z",
        result: "running",
      },
      lastSuccess: OLD_SUCCESS,
    },
  }));
  const unavailable = render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.jobStatus.state_unavailable"))).toBeInTheDocument();
  expect(within(jobRegion()).getByRole("alert").querySelector(".bg-warning")).not.toBeNull();
  expect(within(jobRegion()).getByText("2026-10-08T00:00:00Z")).toBeInTheDocument();
  expect(within(jobRegion()).getByText(i18n.t("selfBackup.attemptResult.running"))).toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobDetectedAt"))).not.toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobFailure"))).not.toBeInTheDocument();
  expect(within(jobRegion()).getByText(OLD_SUCCESS.artifactName)).toBeInTheDocument();
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobStatus.success"))).not.toBeInTheDocument();
  unavailable.unmount();

  getCronBackupStatus.mockResolvedValue(observedBackup({
    job: {
      evidence: "job_record",
      status: "clock_anomaly",
      checkedAt: "2026-10-07T02:00:00Z",
      maxAgeSeconds: 93600,
      latestAttempt: {
        runId: OLD_SUCCESS.runId,
        startedAt: "2026-10-07T03:10:00Z",
        result: "success",
        finishedAt: "2026-10-07T03:00:00Z",
        artifactName: "xirang-sqlite-20261007-030000.db",
      },
      lastSuccess: {
        ...OLD_SUCCESS,
        startedAt: "2026-10-07T03:10:00Z",
        finishedAt: "2026-10-07T03:00:00Z",
        artifactName: "xirang-sqlite-20261007-030000.db",
      },
    },
  }));
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.jobStatus.clock_anomaly"))).toBeInTheDocument();
  expect(within(jobRegion()).getByRole("alert").querySelector(".bg-warning")).not.toBeNull();
  expect(within(jobRegion()).getByRole("alert").querySelector(".bg-success")).toBeNull();
  expect(within(jobRegion()).getAllByText("2026-10-07T03:10:00Z")).toHaveLength(2);
  expect(within(jobRegion()).getAllByText("2026-10-07T03:00:00Z")).toHaveLength(2);
  expect(within(jobRegion()).queryByText(i18n.t("selfBackup.jobStatus.success"))).not.toBeInTheDocument();
  expect(document.querySelector('[data-panel="cron-backup-status"]')?.querySelector("button")).toBeNull();
});
