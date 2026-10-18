import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { flushSync } from "react-dom";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { BackupEntry } from "@/lib/api/system-api";
import { formatBytes } from "@/lib/utils";
import {
  SystemBackupDownloadDialog,
  type BackupDownloadStage,
  type SystemBackupDownloadDialogProps,
} from "./system-backup-download-dialog";

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "session", totpEnabled: false, authTransitioning: false }),
}));

const entry: BackupEntry = {
  source: "cron",
  engine: "postgres",
  filename: "xirang-postgres-20261007-013000.dump",
  size: 2048,
  createdAt: "2026-10-07T01:30:00Z",
  sha256: "b".repeat(64),
  downloadable: true,
  unavailableReason: "",
  packagedSize: 4608,
};

const handlers = {
  onReasonChange: vi.fn(),
  onConfirm: vi.fn(),
  onCancel: vi.fn(),
  onRetry: vi.fn(),
  onCloseAutoFocus: vi.fn(),
};

function dialogProps(overrides: Partial<SystemBackupDownloadDialogProps> = {}): SystemBackupDownloadDialogProps {
  return {
    entry,
    stage: "confirm",
    received: 0,
    total: entry.packagedSize,
    result: null,
    errorMessage: null,
    reason: "",
    reasonError: null,
    reasonMaxLength: 240,
    totpEnabled: true,
    nativeSave: true,
    compatBlocked: false,
    ...handlers,
    ...overrides,
  };
}

function renderDialog(overrides: Partial<SystemBackupDownloadDialogProps> = {}) {
  return render(
    <MemoryRouter>
      <SystemBackupDownloadDialog {...dialogProps(overrides)} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  for (const handler of Object.values(handlers)) handler.mockReset();
});

it("describes the selected backup and confirms from the submit gesture", () => {
  renderDialog({ reason: "copy" });
  const dialog = screen.getByRole("dialog", { name: i18n.t("selfBackup.download.title") });
  expect(within(dialog).getByText(`${entry.source}-${entry.filename}.tar`)).toBeInTheDocument();
  expect(within(dialog).getByText(i18n.t("selfBackup.download.sourceName.cron"))).toBeInTheDocument();
  expect(within(dialog).getByText(i18n.t("selfBackup.engine.postgres"))).toBeInTheDocument();
  expect(within(dialog).getByText(i18n.t("selfBackup.download.saveNative"))).toBeInTheDocument();
  fireEvent.click(within(dialog).getByRole("button", { name: i18n.t("selfBackup.download.confirm") }));
  // Called inside the click itself, so the parent's directory picker retains user activation.
  expect(handlers.onConfirm).toHaveBeenCalledTimes(1);
});

it("does not let an outside click end a running download, while Escape still cancels it", async () => {
  renderDialog({ stage: "preparing" });
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByRole("status")).toHaveTextContent(i18n.t("selfBackup.download.stage.preparing"));
  expect(within(dialog).queryByRole("button", { name: i18n.t("selfBackup.download.confirm") })).not.toBeInTheDocument();
  // Radix registers its outside-pointer listener on the next task. (tsconfig lib is ES2022: no Promise.withResolvers.)
  await new Promise<void>((resolve) => setTimeout(resolve, 0));
  fireEvent.pointerDown(document.body);
  expect(handlers.onCancel).not.toHaveBeenCalled();
  fireEvent.keyDown(dialog, { key: "Escape" });
  expect(handlers.onCancel).toHaveBeenCalledTimes(1);
});

it("reports real byte progress without announcing every chunk", () => {
  renderDialog({ stage: "receiving", received: 2304, total: 4608 });
  const progress = screen.getByRole("progressbar", { name: i18n.t("selfBackup.download.progressLabel") });
  expect(progress).toHaveAttribute("aria-valuenow", "50");
  expect(progress).toHaveAttribute(
    "aria-valuetext",
    i18n.t("selfBackup.download.progressValue", { received: formatBytes(2304), total: formatBytes(4608), percent: 50 }),
  );
  // The byte counter sits outside the live status region; only stage changes are announced.
  expect(screen.getByRole("status")).not.toContainElement(progress);
  expect(screen.getByRole("status")).toHaveTextContent(i18n.t("selfBackup.download.stage.receiving"));
});

it("distinguishes a native save from a browser hand-off and offers an explicit retry after failure", () => {
  const view = renderDialog({ stage: "done", result: "download_started" });
  expect(screen.getByRole("status")).toHaveTextContent(i18n.t("selfBackup.download.doneCompat"));
  expect(screen.queryByText(i18n.t("selfBackup.download.doneNative"))).not.toBeInTheDocument();
  view.unmount();

  renderDialog({ stage: "failed", errorMessage: i18n.t("selfBackup.download.errors.changed") });
  expect(screen.getByRole("alert")).toHaveTextContent(i18n.t("selfBackup.download.errors.changed"));
  expect(screen.queryByRole("button", { name: i18n.t("selfBackup.download.confirm") })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.download.retry") }));
  expect(handlers.onRetry).toHaveBeenCalledTimes(1);
  expect(handlers.onConfirm).not.toHaveBeenCalled();
});

function RetryToConfirmHarness() {
  const [stage, setStage] = useState<BackupDownloadStage>("failed");
  const props = dialogProps({
    stage,
    reason: "copy",
    errorMessage: stage === "failed" ? i18n.t("selfBackup.download.errors.stepUpCancelled") : null,
    onRetry: () => {
      handlers.onRetry();
      // A real click lets React commit before the browser's default action runs; flushSync reproduces that order.
      flushSync(() => setStage("confirm"));
    },
  });
  return (
    <MemoryRouter>
      <SystemBackupDownloadDialog {...props} />
    </MemoryRouter>
  );
}

it("returns to reason confirmation on Try again without submitting a new confirm", () => {
  render(<RetryToConfirmHarness />);
  const retry = screen.getByRole("button", { name: i18n.t("selfBackup.download.retry") });
  fireEvent.click(retry);
  expect(handlers.onRetry).toHaveBeenCalledTimes(1);
  // The retry button must not be re-used as the submit button, or its click would submit the form.
  expect(handlers.onConfirm).not.toHaveBeenCalled();
  const confirm = screen.getByRole("button", { name: i18n.t("selfBackup.download.confirm") });
  expect(screen.getByLabelText(i18n.t("selfBackup.download.reasonLabel"))).toHaveValue("copy");
  fireEvent.click(confirm);
  expect(handlers.onConfirm).toHaveBeenCalledTimes(1);
});

it("blocks confirmation behind the TOTP prerequisite and the compatibility size limit", () => {
  const view = renderDialog({ totpEnabled: false, reason: "copy" });
  expect(screen.getByText(i18n.t("stepUp.prerequisiteNotice"))).toBeInTheDocument();
  expect(screen.getByRole("button", { name: i18n.t("selfBackup.download.confirm") })).toBeDisabled();
  view.unmount();

  renderDialog({ nativeSave: false, compatBlocked: true, reason: "copy" });
  expect(screen.getByText(i18n.t("selfBackup.download.compatTooLarge"))).toBeInTheDocument();
  const confirm = screen.getByRole("button", { name: i18n.t("selfBackup.download.confirm") });
  expect(confirm).toBeDisabled();
  fireEvent.click(confirm);
  expect(handlers.onConfirm).not.toHaveBeenCalled();
});
