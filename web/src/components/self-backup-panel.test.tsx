import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier, formatTime, releaseAuthTransitionBarrier, rememberAuthIdentity } from "@/lib/api/core";
import type { BackupEntry, BackupResult, CronBackupStatus } from "@/lib/api/system-api";
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

const notConfigured: CronBackupStatus = {
  status: "not_configured",
  engine: "sqlite",
  checkedAt: "2026-10-07T00:00:00Z",
  maxAgeSeconds: 93600,
  evidence: "artifact_pair",
  timeSource: "mtime",
  contentVerified: false,
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
};

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
  expect(screen.getByText(i18n.t("selfBackup.status.not_configured"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.thresholdHours", { hours: 26 }))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.contentNotVerified"))).toBeInTheDocument();
  expect(screen.getByText(notConfigured.checkedAt)).toBeInTheDocument();
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
  expect(screen.getByText(i18n.t("selfBackup.status.fresh"))).toBeInTheDocument();
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
  });
  render(<SelfBackupPanel />);
  expect(await screen.findByText(i18n.t("selfBackup.status.invalid_configuration"))).toBeInTheDocument();
  expect(screen.getByText(i18n.t("selfBackup.engine.unknown"))).toBeInTheDocument();
  expect(screen.getByText("2026-10-07T02:00:00.123456789Z")).toBeInTheDocument();
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
