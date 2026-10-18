import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi, type Mock } from "vitest";
import i18n from "@/i18n";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier, formatTime, releaseAuthTransitionBarrier, rememberAuthIdentity } from "@/lib/api/core";
import type { CronBackupStatus, CronJobObservation } from "@/lib/api/cron-backup-status";
import type { BackupEntry, BackupResult } from "@/lib/api/system-api";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { fakeBackupDirectory, fakeBackupLocks } from "@/lib/system-backup-download.test-fixtures";
import { SelfBackupPanel } from "./self-backup-panel";

type ListOptions = { signal?: AbortSignal; source?: "web" | "cron" };

const { auth, webList, cronList, listBackups, backupDB, getCronBackupStatus, requestGrant, downloadBackup, toastSuccess, toastError } = vi.hoisted(() => {
  const webList = vi.fn();
  const cronList = vi.fn();
  return {
    auth: {
      token: "old-session" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
      totpEnabled: true,
      ensureStepUpProof: vi.fn(),
    },
    webList,
    cronList,
    // Routes each source to its own mock so the two lists stay independently scripted.
    listBackups: vi.fn((_token: string, options?: ListOptions) => (options?.source === "cron" ? cronList(options) : webList(options))),
    backupDB: vi.fn(),
    getCronBackupStatus: vi.fn(),
    requestGrant: vi.fn(),
    downloadBackup: vi.fn(),
    toastSuccess: vi.fn(),
    toastError: vi.fn(),
  };
});

vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => auth }));
vi.mock("@/lib/api/client", () => ({
  apiClient: {
    listBackups,
    backupDB,
    getCronBackupStatus,
    requestSystemBackupDownloadCredentialGrant: requestGrant,
    downloadBackup,
  },
}));
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

function entry(filename: string, overrides: Partial<BackupEntry> = {}): BackupEntry {
  return {
    source: "web",
    engine: "sqlite",
    filename,
    size: 4,
    createdAt: "2026-10-07T00:00:00Z",
    sha256: "a".repeat(64),
    downloadable: true,
    unavailableReason: "",
    packagedSize: 3072,
    ...overrides,
  };
}

function cronEntry(filename: string, overrides: Partial<BackupEntry> = {}): BackupEntry {
  return entry(filename, { source: "cron", ...overrides });
}

function envelopeError(status: number, errorCode: string, message = "server detail /var/lib/xirang/secret") {
  return new ApiError(status, message, { code: status, message, data: { error_code: errorCode } });
}

beforeEach(() => {
  clearAuthTransitionBarrier();
  auth.token = "old-session";
  auth.role = "admin";
  auth.totpEnabled = true;
  rememberAuthIdentity(auth.token, auth.role);
  listBackups.mockClear();
  webList.mockReset();
  cronList.mockReset();
  backupDB.mockReset();
  getCronBackupStatus.mockReset();
  requestGrant.mockReset();
  downloadBackup.mockReset();
  auth.ensureStepUpProof.mockReset();
  toastSuccess.mockReset();
  toastError.mockReset();
  webList.mockResolvedValue([]);
  cronList.mockResolvedValue([]);
  getCronBackupStatus.mockResolvedValue(notConfigured);
});

afterEach(() => {
  clearAuthTransitionBarrier();
  rememberAuthIdentity(null, null);
});

it("aborts an old session list and does not show its late response in the new session", async () => {
  let resolveOld!: (value: BackupEntry[]) => void;
  const oldRequest = new Promise<BackupEntry[]>((resolve) => { resolveOld = resolve; });
  webList.mockReturnValueOnce(oldRequest).mockResolvedValueOnce([entry("current.db")]);
  const { rerender } = render(<SelfBackupPanel />);
  const oldSignal: AbortSignal = webList.mock.calls[0][0].signal;
  const oldCronSignal: AbortSignal = cronList.mock.calls[0][0].signal;
  auth.token = "new-session";
  rememberAuthIdentity(auth.token, "admin");
  rerender(<SelfBackupPanel />);
  expect(oldSignal.aborted).toBe(true);
  expect(oldCronSignal.aborted).toBe(true);
  expect(await screen.findByText("current.db")).toBeInTheDocument();
  await act(async () => {
    resolveOld([entry("stale.db", { sha256: "", downloadable: false, unavailableReason: "unmanaged", packagedSize: 0 })]);
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
  webList
    .mockRejectedValueOnce(new ApiError(503, "unavailable"))
    .mockResolvedValueOnce([entry("after-retry.db")]);
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.listLoadFailed"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.noRecords"))).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.retryList") }));
  expect(screen.getByText(i18n.t("selfBackup.loadingList"))).toBeInTheDocument();
  expect(await screen.findByText("after-retry.db")).toBeInTheDocument();
  expect(backupDB).not.toHaveBeenCalled();
});

it("keeps cron evidence and the cron file list available when web self-backup is sqlite-only", async () => {
  webList.mockRejectedValue(new ApiError(501, "当前仅支持 SQLite 数据库备份"));
  cronList.mockResolvedValue([cronEntry("xirang-postgres-20261007-013000.dump", { engine: "postgres" })]);
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
  const cronRegion = screen.getByRole("region", { name: i18n.t("selfBackup.cronFilesTitle") });
  expect(await within(cronRegion).findByRole("button", {
    name: i18n.t("selfBackup.download.rowAction", { filename: "xirang-postgres-20261007-013000.dump" }),
  })).toBeEnabled();
  expect(within(observer as HTMLElement).queryByRole("button")).not.toBeInTheDocument();
  expect(cronList).toHaveBeenCalledWith(expect.objectContaining({ source: "cron" }));
});

it("shows cron evidence while the web list is still loading", async () => {
  const pending = deferred<BackupEntry[]>();
  webList.mockReturnValueOnce(pending.promise);
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
  webList.mockResolvedValueOnce([]).mockRejectedValueOnce(new ApiError(500, "refresh failed"));
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
  webList.mockResolvedValue([entry("web.db")]);
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

// ── R-14 system backup download ──

type Bytes = Uint8Array<ArrayBuffer>;
type ReadStep = Bytes | Promise<ReadableStreamReadResult<Bytes>>;

const WEB_FILE = "xirang-20261007-000000.db";
const CRON_FILE = "xirang-sqlite-20261007-010000.db";

function bytes(...values: number[]): Bytes {
  return new Uint8Array(values);
}

function tarResponse(steps: ReadStep[], total: number) {
  const reads = [...steps];
  const reader = {
    read: vi.fn((): Promise<ReadableStreamReadResult<Bytes>> => {
      const step = reads.shift();
      if (step === undefined) return Promise.resolve({ done: true, value: undefined });
      if (step instanceof Promise) return step;
      return Promise.resolve({ done: false, value: step });
    }),
    cancel: vi.fn(async () => undefined),
    releaseLock: vi.fn(),
  };
  const body = { locked: false, getReader: vi.fn(() => reader), cancel: vi.fn(async () => undefined) };
  // Scripted body: the download helper only uses status, headers and the reader.
  const response = {
    status: 200,
    headers: new Headers({ "content-type": "application/x-tar", "content-length": String(total) }),
    body,
  } as unknown as Response;
  return { response, reader };
}

type FakeWriter = {
  write: Mock<(chunk: Bytes) => Promise<void>>;
  close: Mock<() => Promise<void>>;
  abort: Mock<() => Promise<void>>;
};

function installPicker(pick?: () => Promise<unknown>) {
  const writer: FakeWriter = {
    write: vi.fn(async () => undefined),
    close: vi.fn(async () => undefined),
    abort: vi.fn(async () => undefined),
  };
  const createWritable = vi.fn(async () => writer);
  const directory = fakeBackupDirectory(createWritable);
  const picker = vi.fn(pick ?? (async () => directory));
  const locks = fakeBackupLocks();
  Object.defineProperty(navigator, "locks", { configurable: true, value: locks });
  Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
  Object.defineProperty(window, "showDirectoryPicker", { configurable: true, writable: true, value: picker });
  return { picker, writer, createWritable, directory, locks };
}

function installCompatDownload() {
  Reflect.deleteProperty(window, "showDirectoryPicker");
  const createObjectURL = vi.fn(() => "blob:r14-panel");
  Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: createObjectURL });
  Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: vi.fn() });
  const clicks: string[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicks.push(this.download);
  });
  return { createObjectURL, clicks };
}

afterEach(() => {
  Reflect.deleteProperty(window, "showDirectoryPicker");
  Reflect.deleteProperty(navigator, "locks");
  Reflect.deleteProperty(window, "isSecureContext");
  vi.restoreAllMocks();
});

function renderPanel(ui: ReactElement = <SelfBackupPanel />) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

async function openDownloadFor(filename: string) {
  const button = await screen.findByRole("button", { name: i18n.t("selfBackup.download.rowAction", { filename }) });
  fireEvent.click(button);
  const dialog = await screen.findByRole("dialog", { name: i18n.t("selfBackup.download.title") });
  return { button, dialog };
}

function enterReason(value: string) {
  fireEvent.change(screen.getByLabelText(i18n.t("selfBackup.download.reasonLabel")), { target: { value } });
}

function confirmButton(): HTMLElement {
  return screen.getByRole("button", { name: i18n.t("selfBackup.download.confirm") });
}

function errorText(key: string): string {
  return i18n.t(`selfBackup.download.errors.${key}`);
}

it("opens the directory picker inside the confirm gesture, then proves, grants, streams and reports a native save", async () => {
  const { picker, writer, directory } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE, { packagedSize: 4 })]);
  auth.ensureStepUpProof.mockResolvedValue("proof-1");
  requestGrant.mockResolvedValue({ id: 9, status: "active" });
  downloadBackup.mockResolvedValue(tarResponse([bytes(1, 2), bytes(3, 4)], 4).response);
  renderPanel();
  const { button } = await openDownloadFor(WEB_FILE);
  expect(screen.getByText(i18n.t("selfBackup.download.sensitive"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.download.keysSeparate"))).toBeInTheDocument();
  enterReason("  monthly copy  ");
  const confirm = confirmButton();
  fireEvent.click(confirm);
  fireEvent.click(confirm);

  // The picker ran synchronously in the click, before any proof request was awaited.
  expect(picker).toHaveBeenCalledTimes(1);
  expect(auth.ensureStepUpProof).not.toHaveBeenCalled();

  expect(await screen.findByText(i18n.t("selfBackup.download.doneNative"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.download.doneCompat"))).not.toBeInTheDocument();
  expect(directory.entries.has(`web-${WEB_FILE}.tar`)).toBe(true);
  expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(1);
  expect(auth.ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.systemBackupDownload, {
    persist: false,
    reuseCached: false,
    signal: expect.any(AbortSignal),
  });
  expect(requestGrant).toHaveBeenCalledWith(
    "old-session",
    { reason: "monthly copy", requestedTtlSeconds: 600 },
    "proof-1",
    expect.objectContaining({ signal: expect.any(AbortSignal) }),
  );
  expect(downloadBackup).toHaveBeenCalledTimes(1);
  expect(downloadBackup.mock.calls[0][1]).toMatchObject({ source: "web", filename: WEB_FILE, sha256: "a".repeat(64) });
  expect(downloadBackup.mock.calls[0][2]).toBe("proof-1");
  expect(writer.write).toHaveBeenCalledTimes(2);
  expect(writer.close).toHaveBeenCalledTimes(1);
  expect(writer.abort).not.toHaveBeenCalled();

  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.download.close") }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  await waitFor(() => expect(button).toHaveFocus());
});

it("refuses an archive over 64 MiB without native directory saving before any proof, grant or download", async () => {
  installCompatDownload();
  webList.mockResolvedValue([entry(WEB_FILE, { packagedSize: 64 * 1024 * 1024 + 512 })]);
  renderPanel();
  await openDownloadFor(WEB_FILE);
  expect(screen.getByText(i18n.t("selfBackup.download.compatTooLarge"))).toBeInTheDocument();
  expect(confirmButton()).toBeDisabled();
  fireEvent.click(confirmButton());
  expect(auth.ensureStepUpProof).not.toHaveBeenCalled();
  expect(requestGrant).not.toHaveBeenCalled();
  expect(downloadBackup).not.toHaveBeenCalled();
});

it("hands a compatible archive to the browser download and never calls it saved", async () => {
  const { clicks } = installCompatDownload();
  cronList.mockResolvedValue([cronEntry(CRON_FILE, { packagedSize: 3 })]);
  auth.ensureStepUpProof.mockResolvedValue("proof-c");
  requestGrant.mockResolvedValue({ id: 3, status: "active" });
  downloadBackup.mockResolvedValue(tarResponse([bytes(7, 8, 9)], 3).response);
  renderPanel();
  await openDownloadFor(CRON_FILE);
  expect(screen.getByText(i18n.t("selfBackup.download.saveCompat"))).toBeInTheDocument();
  enterReason("restore drill");
  fireEvent.click(confirmButton());
  expect(await screen.findByText(i18n.t("selfBackup.download.doneCompat"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.download.doneNative"))).not.toBeInTheDocument();
  expect(clicks).toEqual([`cron-${CRON_FILE}.tar`]);
  expect(downloadBackup.mock.calls[0][1]).toMatchObject({ source: "cron", filename: CRON_FILE });
});

it("requires a reason and shows the TOTP prerequisite instead of requesting anything", async () => {
  const { picker } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE)]);
  const view = renderPanel();
  await openDownloadFor(WEB_FILE);
  fireEvent.click(confirmButton());
  expect(await screen.findByText(i18n.t("selfBackup.download.reasonRequired"))).toBeInTheDocument();
  expect(picker).not.toHaveBeenCalled();
  expect(auth.ensureStepUpProof).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.download.cancel") }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  view.unmount();

  auth.totpEnabled = false;
  renderPanel();
  await openDownloadFor(WEB_FILE);
  expect(screen.getByText(i18n.t("stepUp.prerequisiteNotice"))).toBeInTheDocument();
  expect(confirmButton()).toBeDisabled();
  fireEvent.click(confirmButton());
  expect(picker).not.toHaveBeenCalled();
  expect(auth.ensureStepUpProof).not.toHaveBeenCalled();
  expect(requestGrant).not.toHaveBeenCalled();
});

it("keeps a cancelled TOTP prompt as a failure and only retries after a new explicit confirm", async () => {
  const first = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE)]);
  auth.ensureStepUpProof.mockRejectedValueOnce(new Error(i18n.t("stepUp.cancelled")));
  renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  expect(await screen.findByText(errorText("stepUpCancelled"))).toBeInTheDocument();
  expect(first.writer.abort).toHaveBeenCalled();
  expect(requestGrant).not.toHaveBeenCalled();
  expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(1);

  const proof = deferred<string>();
  // An explicit retry can select a different directory; the prior empty entry remains.
  const nextDirectory = fakeBackupDirectory(first.createWritable);
  first.picker.mockResolvedValueOnce(nextDirectory);
  auth.ensureStepUpProof.mockReturnValueOnce(proof.promise);
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.download.retry") }));
  expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText(i18n.t("selfBackup.download.reasonLabel"))).toHaveValue("copy");
  fireEvent.click(confirmButton());
  expect(first.picker).toHaveBeenCalledTimes(2);
  await waitFor(() => expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(2));
  expect(await screen.findByText(i18n.t("selfBackup.download.stage.verifying"))).toBeInTheDocument();
});

it("cancels an in-flight grant, aborts the save target and ignores the late grant", async () => {
  const { writer } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE)]);
  auth.ensureStepUpProof.mockResolvedValue("proof-1");
  const grant = deferred<{ id: number }>();
  requestGrant.mockReturnValueOnce(grant.promise);
  renderPanel();
  const { button } = await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  await waitFor(() => expect(requestGrant).toHaveBeenCalledTimes(1));
  expect(await screen.findByText(i18n.t("selfBackup.download.stage.preparing"))).toBeInTheDocument();
  const grantSignal: AbortSignal = requestGrant.mock.calls[0][3].signal;
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.download.cancelActive") }));
  expect(grantSignal.aborted).toBe(true);
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  await act(async () => {
    grant.resolve({ id: 1 });
  });
  expect(downloadBackup).not.toHaveBeenCalled();
  await waitFor(() => expect(writer.abort).toHaveBeenCalled());
  expect(writer.close).not.toHaveBeenCalled();
  await waitFor(() => expect(button).toHaveFocus());
});

it("aborts the owned proof prompt on Escape without replaying it", async () => {
  const { writer } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE)]);
  auth.ensureStepUpProof.mockReturnValueOnce(new Promise<string>(() => undefined));
  renderPanel();
  const { dialog } = await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  await waitFor(() => expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(1));
  const proofSignal: AbortSignal = auth.ensureStepUpProof.mock.calls[0][1].signal;
  fireEvent.keyDown(dialog, { key: "Escape" });
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(proofSignal.aborted).toBe(true);
  await waitFor(() => expect(writer.abort).toHaveBeenCalled());
  expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(1);
  expect(requestGrant).not.toHaveBeenCalled();
});

it("drops a download whose session generation changed before headers arrived", async () => {
  const { writer } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE, { packagedSize: 2 })]);
  auth.ensureStepUpProof.mockResolvedValue("proof-1");
  requestGrant.mockResolvedValue({ id: 1 });
  const pending = deferred<Response>();
  downloadBackup.mockReturnValueOnce(pending.promise);
  renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  await waitFor(() => expect(downloadBackup).toHaveBeenCalledTimes(1));
  bumpAuthSessionGeneration();
  const { response, reader } = tarResponse([bytes(1, 2)], 2);
  await act(async () => {
    pending.resolve(response);
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(reader.read).not.toHaveBeenCalled();
  expect(writer.write).not.toHaveBeenCalled();
  expect(writer.close).not.toHaveBeenCalled();
  expect(writer.abort).toHaveBeenCalled();
  expect(screen.queryByText(i18n.t("selfBackup.download.doneNative"))).not.toBeInTheDocument();
});

it("aborts the running download when the panel unmounts", async () => {
  const { writer } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE)]);
  auth.ensureStepUpProof.mockReturnValueOnce(new Promise<string>(() => undefined));
  const view = renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  await waitFor(() => expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(1));
  const proofSignal: AbortSignal = auth.ensureStepUpProof.mock.calls[0][1].signal;
  view.unmount();
  expect(proofSignal.aborted).toBe(true);
  await waitFor(() => expect(writer.abort).toHaveBeenCalled());
});

it.each([
  ["download changed", "download", envelopeError(412, "BACKUP_CHANGED"), "changed"],
  ["server busy", "download", envelopeError(429, "BACKUP_DOWNLOAD_BUSY"), "busy"],
  ["integrity failure", "download", envelopeError(409, "BACKUP_INTEGRITY_FAILED"), "integrityFailed"],
  ["source not configured", "download", envelopeError(409, "BACKUP_SOURCE_NOT_CONFIGURED"), "sourceNotConfigured"],
  ["prepare timeout", "download", envelopeError(504, "BACKUP_PREPARE_TIMEOUT"), "timeout"],
  ["grant reason rejected", "grant", new ApiError(400, "reason must be 1-240 characters"), "reasonRejected"],
  ["grant missing", "download", envelopeError(403, "CREDENTIAL_GRANT_REQUIRED"), "grantRequired"],
] as const)("explains %s with fixed text and aborts the save target", async (_label, step, error, key) => {
  const { writer } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE)]);
  auth.ensureStepUpProof.mockResolvedValue("proof-1");
  if (step === "grant") requestGrant.mockRejectedValueOnce(error);
  else {
    requestGrant.mockResolvedValue({ id: 1 });
    downloadBackup.mockRejectedValueOnce(error);
  }
  renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  expect(await screen.findByText(errorText(key))).toBeInTheDocument();
  expect(screen.getByRole("alert")).not.toHaveTextContent(error.message);
  expect(writer.abort).toHaveBeenCalled();
  expect(writer.close).not.toHaveBeenCalled();
  if (step === "grant") expect(downloadBackup).not.toHaveBeenCalled();
});

it("reports a dismissed directory picker and a disk write failure without asking for proof again", async () => {
  installPicker(async () => {
    throw new DOMException("dismissed", "AbortError");
  });
  webList.mockResolvedValue([entry(WEB_FILE, { packagedSize: 2 })]);
  const view = renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  expect(await screen.findByText(errorText("pickerCancelled"))).toBeInTheDocument();
  expect(auth.ensureStepUpProof).not.toHaveBeenCalled();
  view.unmount();

  const { writer } = installPicker();
  writer.write.mockRejectedValueOnce(new Error("disk full"));
  auth.ensureStepUpProof.mockResolvedValue("proof-1");
  requestGrant.mockResolvedValue({ id: 1 });
  downloadBackup.mockResolvedValue(tarResponse([bytes(1, 2)], 2).response);
  renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  expect(await screen.findByText(errorText("writeFailed"))).toBeInTheDocument();
  expect(writer.close).not.toHaveBeenCalled();
  expect(auth.ensureStepUpProof).toHaveBeenCalledTimes(1);
});

it("shows real received bytes, then the commit stage, and only reports success after close settles", async () => {
  const { writer } = installPicker();
  const close = deferred<void>();
  writer.close.mockReturnValueOnce(close.promise);
  webList.mockResolvedValue([entry(WEB_FILE, { packagedSize: 4 })]);
  auth.ensureStepUpProof.mockResolvedValue("proof-1");
  requestGrant.mockResolvedValue({ id: 1 });
  const secondChunk = deferred<ReadableStreamReadResult<Bytes>>();
  downloadBackup.mockResolvedValue(tarResponse([bytes(1, 2), secondChunk.promise], 4).response);
  renderPanel();
  const { dialog } = await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  const progress = await screen.findByRole("progressbar", { name: i18n.t("selfBackup.download.progressLabel") });
  await waitFor(() => expect(progress).toHaveAttribute("aria-valuenow", "50"));
  expect(within(dialog).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.download.stage.receiving"));
  await act(async () => {
    secondChunk.resolve({ done: false, value: bytes(3, 4) });
  });
  await waitFor(() => expect(within(dialog).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.download.stage.committing")));
  expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
  expect(screen.queryByText(i18n.t("selfBackup.download.doneNative"))).not.toBeInTheDocument();
  await act(async () => {
    close.resolve();
  });
  expect(await screen.findByText(i18n.t("selfBackup.download.doneNative"))).toBeInTheDocument();
});

it("returns focus to the source heading when the originating row is gone", async () => {
  installPicker();
  cronList.mockResolvedValueOnce([cronEntry(CRON_FILE)]).mockResolvedValueOnce([]);
  renderPanel();
  await openDownloadFor(CRON_FILE);
  const cronRegion = screen.getByRole("region", { name: i18n.t("selfBackup.cronFilesTitle"), hidden: true });
  fireEvent.click(within(cronRegion).getByRole("button", { name: i18n.t("selfBackup.refreshCron"), hidden: true }));
  expect(await within(cronRegion).findByText(i18n.t("selfBackup.cronFilesEmpty"))).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.download.cancel") }));
  await waitFor(() => expect(document.getElementById("self-backup-cron-heading")).toHaveFocus());
});

it("lists unavailable rows with their reason and explains an unconfigured cron source", async () => {
  webList.mockResolvedValue([
    entry("manual-copy.db", { sha256: "", downloadable: false, unavailableReason: "unmanaged", packagedSize: 0 }),
    entry("xirang-20261007-000001.db", { sha256: "", downloadable: false, unavailableReason: "incomplete", packagedSize: 0 }),
  ]);
  cronList.mockRejectedValue(envelopeError(409, "BACKUP_SOURCE_NOT_CONFIGURED"));
  renderPanel();
  const webRegion = screen.getByRole("region", { name: i18n.t("selfBackup.webListTitle") });
  expect(await within(webRegion).findByText("manual-copy.db")).toBeInTheDocument();
  expect(within(webRegion).getByText(i18n.t("selfBackup.download.unavailable.unmanaged"))).toBeInTheDocument();
  expect(within(webRegion).getByText(i18n.t("selfBackup.download.unavailable.incomplete"))).toBeInTheDocument();
  expect(within(webRegion).queryByRole("button", { name: /manual-copy\.db/ })).not.toBeInTheDocument();
  expect(await screen.findByText(i18n.t("selfBackup.cronFilesNotConfigured"))).toBeInTheDocument();
  expect(screen.queryByText(i18n.t("selfBackup.cronFilesLoadFailed"))).not.toBeInTheDocument();
});

it.each(["file", "empty", "directory"] as const)("rejects an existing %s before proof, grant or download", async (kind) => {
  const { directory, createWritable } = installPicker();
  const item = { kind: kind === "directory" ? "directory" as const : "file" as const, bytes: kind === "file" ? [42] : [], mtime: 77 };
  directory.entries.set(`web-${WEB_FILE}.tar`, item);
  webList.mockResolvedValue([entry(WEB_FILE)]);
  renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  expect(await screen.findByText(errorText("targetExists"))).toBeInTheDocument();
  expect(directory.entries.get(`web-${WEB_FILE}.tar`)).toEqual(item);
  expect(createWritable).not.toHaveBeenCalled();
  expect(auth.ensureStepUpProof).not.toHaveBeenCalled();
  expect(requestGrant).not.toHaveBeenCalled();
  expect(downloadBackup).not.toHaveBeenCalled();
});

it.each(["generation", "A-B-A", "barrier"] as const)("resets old confirmation, rows and observer after same-batch %s invalidation", async (change) => {
  installPicker();
  const newRows = deferred<BackupEntry[]>();
  webList.mockResolvedValueOnce([entry(WEB_FILE)]).mockReturnValueOnce(newRows.promise);
  getCronBackupStatus.mockResolvedValueOnce(freshPostgres).mockReturnValueOnce(deferred<CronBackupStatus>().promise);
  renderPanel();
  await screen.findByText(freshPostgres.artifactName ?? "");
  await openDownloadFor(WEB_FILE);
  enterReason("old secret reason");
  act(() => {
    if (change === "barrier") {
      const barrier = beginAuthTransitionBarrier();
      releaseAuthTransitionBarrier(barrier);
    } else {
      if (change === "A-B-A") {
        rememberAuthIdentity("other-token", "admin");
        bumpAuthSessionGeneration();
        rememberAuthIdentity("old-session", "admin");
      }
      bumpAuthSessionGeneration();
    }
  });
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByText(WEB_FILE)).not.toBeInTheDocument();
  expect(screen.queryByText(freshPostgres.artifactName ?? "")).not.toBeInTheDocument();
  expect(webList).toHaveBeenCalledTimes(2);
  expect(cronList).toHaveBeenCalledTimes(2);
  expect(getCronBackupStatus).toHaveBeenCalledTimes(2);
  expect(auth.ensureStepUpProof).not.toHaveBeenCalled();
  expect(backupDB).not.toHaveBeenCalled();
  await act(async () => { newRows.resolve([entry(WEB_FILE)]); });
  await openDownloadFor(WEB_FILE);
  expect(screen.getByLabelText(i18n.t("selfBackup.download.reasonLabel"))).toHaveValue("");
});

it("generation alone immediately cancels a stalled read and releases its sink without waiting for read settlement", async () => {
  const { writer, locks } = installPicker();
  webList.mockResolvedValue([entry(WEB_FILE, { packagedSize: 2 })]);
  auth.ensureStepUpProof.mockResolvedValue("proof");
  requestGrant.mockResolvedValue({ id: 1 });
  const stalled = deferred<ReadableStreamReadResult<Bytes>>();
  const { response, reader } = tarResponse([stalled.promise], 2);
  downloadBackup.mockResolvedValue(response);
  renderPanel();
  await openDownloadFor(WEB_FILE);
  enterReason("copy");
  fireEvent.click(confirmButton());
  await waitFor(() => expect(reader.read).toHaveBeenCalled());
  const signal: AbortSignal = downloadBackup.mock.calls[0][3].signal;
  act(() => { bumpAuthSessionGeneration(); });
  expect(signal.aborted).toBe(true);
  expect(reader.cancel).toHaveBeenCalled();
  await waitFor(() => expect(locks.held.size).toBe(0));
  expect(writer.abort).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await act(async () => { stalled.resolve({ done: false, value: bytes(1, 2) }); });
  expect(writer.close).not.toHaveBeenCalled();
  expect(writer.write).not.toHaveBeenCalled();
});
