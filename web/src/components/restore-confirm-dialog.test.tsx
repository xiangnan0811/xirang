import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { RestoreConfirmDialog } from "./restore-confirm-dialog";

const {
  ensureStepUpProofMock,
  requestTaskRestoreCredentialGrantMock,
  restoreTaskMock,
  onOpenChangeMock,
  onSuccessMock,
} = vi.hoisted(() => ({
  ensureStepUpProofMock: vi.fn(),
  requestTaskRestoreCredentialGrantMock: vi.fn(),
  restoreTaskMock: vi.fn(),
  onOpenChangeMock: vi.fn(),
  onSuccessMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    ensureStepUpProof: ensureStepUpProofMock,
  }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    requestTaskRestoreCredentialGrant: requestTaskRestoreCredentialGrantMock,
    restoreTask: restoreTaskMock,
  },
}));

describe("RestoreConfirmDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    ensureStepUpProofMock.mockResolvedValue("step-up-marker");
    requestTaskRestoreCredentialGrantMock.mockResolvedValue({ id: 9, status: "active" });
    restoreTaskMock.mockResolvedValue({ runId: 88 });
  });

  function renderDialog() {
    render(
      <RestoreConfirmDialog
        open
        onOpenChange={onOpenChangeMock}
        taskId={101}
        taskName="重要备份"
        rsyncSource="/data/source"
        rsyncTarget="/backup/target"
        token="auth-marker"
        onSuccess={onSuccessMock}
      />,
    );
  }

  it("renders the existing restore fields when opened", () => {
    renderDialog();

    expect(screen.getByRole("dialog", { name: "备份恢复" })).toBeInTheDocument();
    expect(screen.getByText("/data/source")).toBeInTheDocument();
    expect(screen.getByText("/backup/target")).toBeInTheDocument();
    expect(screen.getByLabelText("恢复目标路径")).toHaveValue("/data/source");
    expect(screen.getByLabelText("授权原因")).toHaveValue("");
  });

  it("requires a local-only reason before requesting a task restore grant", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "申请授权并恢复" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("请填写授权原因。");
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestTaskRestoreCredentialGrantMock).not.toHaveBeenCalled();
    expect(restoreTaskMock).not.toHaveBeenCalled();
  });

  it("blocks too-long reasons locally before requesting proof", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.type(screen.getByLabelText("授权原因"), "测".repeat(241));
    await user.click(screen.getByRole("button", { name: "申请授权并恢复" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("授权原因不能超过 240 个字符。");
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestTaskRestoreCredentialGrantMock).not.toHaveBeenCalled();
    expect(restoreTaskMock).not.toHaveBeenCalled();
  });

  it("requests one-time step-up and task grant before restoring with the same proof without storing material", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.clear(screen.getByLabelText("恢复目标路径"));
    await user.type(screen.getByLabelText("恢复目标路径"), "/restore-target");
    await user.type(screen.getByLabelText("授权原因"), "恢复误删目录");
    await user.click(screen.getByRole("button", { name: "申请授权并恢复" }));

    await waitFor(() => expect(requestTaskRestoreCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(ensureStepUpProofMock).toHaveBeenCalledWith({ persist: false, reuseCached: false });
    expect(apiClient.requestTaskRestoreCredentialGrant).toHaveBeenCalledWith(
      "auth-marker",
      { taskId: 101, reason: "恢复误删目录", requestedTtlSeconds: 600 },
      "step-up-marker",
    );
    expect(apiClient.restoreTask).toHaveBeenCalledWith("auth-marker", 101, "/restore-target", "step-up-marker");
    expect(requestTaskRestoreCredentialGrantMock.mock.invocationCallOrder[0]).toBeLessThan(restoreTaskMock.mock.invocationCallOrder[0]);
    await waitFor(() => expect(onOpenChangeMock).toHaveBeenCalledWith(false));
    expect(onSuccessMock).toHaveBeenCalledWith(88);

    const browserStorage = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(browserStorage).not.toContain("恢复误删目录");
    expect(browserStorage).not.toContain("CREDENTIAL_GRANT_REQUIRED");
    expect(browserStorage).not.toContain("active");
    expect(browserStorage).not.toContain("9");
    expect(browserStorage).not.toContain("101");
    expect(browserStorage).not.toContain("88");
    expect(browserStorage).not.toContain("/restore-target");
    expect(browserStorage).not.toContain("step-up-marker");
  });

  it("renders grant errors as text and does not restore", async () => {
    const user = userEvent.setup();
    requestTaskRestoreCredentialGrantMock.mockRejectedValueOnce(new Error("需要临时授权 <script>alert(1)</script>"));
    renderDialog();

    await user.type(screen.getByLabelText("授权原因"), "恢复误删目录");
    await user.click(screen.getByRole("button", { name: "申请授权并恢复" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("需要临时授权 <script>alert(1)</script>");
    expect(alert.innerHTML).not.toContain("<script>");
    expect(restoreTaskMock).not.toHaveBeenCalled();
  });
});
