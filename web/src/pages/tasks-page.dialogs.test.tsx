import "@testing-library/jest-dom/vitest";
import { useRef, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { dialogOpenerFromTarget, restoreConnectedDialogOpener } from "@/components/ui/dialog-opener";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import i18n from "@/i18n";
import { ApiError } from "@/lib/api/core";
import type { TaskRecord } from "@/types/domain";
import { TasksPageDialogs, type TasksPageDialogsProps } from "./tasks-page.dialogs";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    createRsyncVersioningPreflight: vi.fn(),
    activateRsyncVersioning: vi.fn(),
    prepareRsyncVersioningRollback: vi.fn(),
    connectBackupRepository: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({ apiClient: apiClientMock }));
vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "token", role: "admin" }),
}));
vi.mock("@/components/batch-command-dialog", () => ({ BatchCommandDialog: () => null }));
vi.mock("@/components/batch-result-dialog", () => ({ BatchResultDialog: () => null }));
vi.mock("@/components/task-rclone-versioning-dialog", () => ({ TaskRcloneVersioningDialog: () => null }));
vi.mock("@/components/task-run-history", () => ({ TaskRunHistory: () => null }));
vi.mock("@/components/task-run-detail", () => ({ TaskRunDetail: () => null }));
vi.mock("@/features/backup-assets/backup-assets-task-context-link", () => ({ BackupAssetsTaskContextLink: () => null }));

const snapshot: TaskRecord = {
  id: 7,
  name: "nightly-rsync",
  policyName: "nightly-rsync",
  nodeId: 1,
  nodeName: "backup-node",
  status: "pending",
  progress: 0,
  startedAt: "-",
  speedMbps: 0,
  enabled: true,
  executorType: "rsync",
  rsyncPublication: {
    mode: "legacy_mutable",
    state: "legacy",
    reasonCode: "legacy",
    capabilityRevision: 7,
    taskRevision: "9",
    seedFullCopyRequired: false,
  },
};

const noop = () => undefined;
const asyncNoop = async () => undefined;

// Mirrors TasksPage: one opener ref serves both dialogs, so a versioning handoff restores to its opener.
function Harness({ tasks }: { tasks: TaskRecord[] }) {
  const [rsyncVersioningTask, setRsyncVersioningTask] = useState<TaskRecord | null>(null);
  const [previewConnectTask, setPreviewConnectTask] = useState<TaskRecord | null>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const restoreOpener: DialogCloseAutoFocus = (event) => restoreConnectedDialogOpener(event, openerRef.current);
  const props: TasksPageDialogsProps = {
    createDialogOpen: false,
    setCreateDialogOpen: noop,
    editDialogOpen: false,
    setEditDialogOpen: noop,
    editingTask: null,
    setEditingTask: noop,
    historyTask: null,
    setHistoryTask: noop,
    selectedRun: null,
    setSelectedRun: noop,
    batchDialogOpen: false,
    setBatchDialogOpen: noop,
    batchDefaultNodeIds: undefined,
    setBatchDefaultNodeIds: noop,
    batchResultId: null,
    setBatchResultId: noop,
    batchRetain: false,
    setBatchRetain: noop,
    rsyncVersioningTask,
    setRsyncVersioningTask,
    rsyncVersioningOnCloseAutoFocus: restoreOpener,
    canManageRsyncVersioning: true,
    onRsyncVersioningUpdated: asyncNoop,
    previewConnectTask,
    setPreviewConnectTask,
    previewConnectOnCloseAutoFocus: restoreOpener,
    canConnectTaskPreview: true,
    rcloneVersioningTask: null,
    setRcloneVersioningTask: noop,
    canManageRcloneVersioning: false,
    onRcloneVersioningUpdated: asyncNoop,
    nodes: [],
    policies: [],
    tasks,
    authToken: "token",
    canWriteTasks: false,
    handleCreateTask: asyncNoop,
    handleUpdateTask: asyncNoop,
    pauseConfirmTask: null,
    setPauseConfirmTask: noop,
    onConfirmPause: asyncNoop,
    onSkipNext: asyncNoop,
  };
  return (
    <>
      <button
        type="button"
        onClick={(event) => {
          openerRef.current = dialogOpenerFromTarget(event.currentTarget);
          setRsyncVersioningTask(snapshot);
        }}
      >
        open versioning
      </button>
      <TasksPageDialogs {...props} />
    </>
  );
}

const sshSourceError = new ApiError(501, "rsync: ssh backup@10.0.0.5 /private/secret", {
  code: 501,
  data: { reason: { code: "local_source_required", params: {} } },
});

// fireEvent.click dispatches without focusing the trigger, like Safari/macOS pointer clicks, so focus
// return must come from the captured opener rather than whatever was active when the dialog opened.
async function openVersioning(user: UserEvent) {
  const opener = screen.getByRole("button", { name: "open versioning" });
  fireEvent.click(opener);
  expect(opener).not.toHaveFocus();
  await user.click(await screen.findByRole("button", { name: i18n.t("rsyncVersioning.runPreflight") }));
}

async function openVersioningWithSshSource(user: UserEvent) {
  apiClientMock.createRsyncVersioningPreflight.mockRejectedValueOnce(sshSourceError);
  await openVersioning(user);
  return screen.findByRole("button", { name: i18n.t("rsyncVersioning.connectPreview") });
}

describe("TasksPageDialogs rsync versioning preview handoff", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("opens the live task's preview connect dialog without connecting and restores focus to the opener", async () => {
    const user = userEvent.setup();
    const live = { ...snapshot, name: "nightly-rsync-live" };
    render(<Harness tasks={[live]} />);

    await user.click(await openVersioningWithSshSource(user));

    expect(await screen.findByText(i18n.t("taskPreviewConnect.title", { name: "nightly-rsync-live" }))).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("rsyncVersioning.dialogTitle", { name: "nightly-rsync" }))).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: i18n.t("taskPreviewConnect.confirm") })).toBeEnabled();
    expect(apiClientMock.connectBackupRepository).not.toHaveBeenCalled();

    await user.click(screen.getAllByRole("button", { name: i18n.t("common.close") })[0]);
    await waitFor(() => expect(screen.getByRole("button", { name: "open versioning" })).toHaveFocus());
    expect(apiClientMock.connectBackupRepository).not.toHaveBeenCalled();
  });

  it("restores focus to an unfocused pointer opener when versioning closes without a handoff", async () => {
    const user = userEvent.setup();
    render(<Harness tasks={[snapshot]} />);
    apiClientMock.createRsyncVersioningPreflight.mockRejectedValueOnce(sshSourceError);
    await openVersioning(user);
    expect(await screen.findByRole("button", { name: i18n.t("rsyncVersioning.connectPreview") })).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.getByRole("button", { name: "open versioning" })).toHaveFocus());
    expect(screen.queryByText(i18n.t("taskPreviewConnect.title", { name: "nightly-rsync" }))).not.toBeInTheDocument();
  });

  it("uses the live active-run state instead of the stale dialog snapshot", async () => {
    const user = userEvent.setup();
    render(<Harness tasks={[{ ...snapshot, hasActiveRun: true }]} />);

    const connect = await openVersioningWithSshSource(user);
    expect(connect).toHaveAttribute("aria-disabled", "true");
    await user.click(connect);

    expect(screen.queryByText(i18n.t("taskPreviewConnect.title", { name: "nightly-rsync" }))).not.toBeInTheDocument();
    expect(screen.getByText(i18n.t("rsyncVersioning.dialogTitle", { name: "nightly-rsync" }))).toBeInTheDocument();
  });

  it("offers no handoff once the task is no longer eligible", async () => {
    const user = userEvent.setup();
    render(<Harness tasks={[]} />);
    apiClientMock.createRsyncVersioningPreflight.mockRejectedValueOnce(sshSourceError);
    await openVersioning(user);

    expect(await screen.findByText(i18n.t("rsyncVersioning.notice.local_source_required"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: i18n.t("rsyncVersioning.connectPreview") })).not.toBeInTheDocument();
  });
});
