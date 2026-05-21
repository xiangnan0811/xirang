import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { SnapshotBrowser } from "./snapshot-browser";

const {
  requestOneTimeStepUpProofMock,
  listSnapshotsMock,
  listSnapshotFilesMock,
  requestSnapshotRestoreCredentialGrantMock,
  restoreSnapshotMock,
  toastSuccessMock,
  toastErrorMock,
} = vi.hoisted(() => ({
  requestOneTimeStepUpProofMock: vi.fn(),
  listSnapshotsMock: vi.fn(),
  listSnapshotFilesMock: vi.fn(),
  requestSnapshotRestoreCredentialGrantMock: vi.fn(),
  restoreSnapshotMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    ensureStepUpProof: requestOneTimeStepUpProofMock,
  }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    listSnapshots: listSnapshotsMock,
    listSnapshotFiles: listSnapshotFilesMock,
    requestSnapshotRestoreCredentialGrant: requestSnapshotRestoreCredentialGrantMock,
    restoreSnapshot: restoreSnapshotMock,
  },
}));

vi.mock("sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

describe("SnapshotBrowser", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    requestOneTimeStepUpProofMock.mockResolvedValue("step-up-marker");
    requestSnapshotRestoreCredentialGrantMock.mockResolvedValue({ id: 9, status: "active" });
    restoreSnapshotMock.mockResolvedValue(undefined);
    listSnapshotsMock.mockResolvedValue([
      { id: "abcdef1234567890", short_id: "abcdef12", time: "2026-05-20T00:00:00Z", hostname: "", paths: [] },
    ]);
    listSnapshotFilesMock.mockResolvedValue([
      { name: "safe-file", type: "file", path: "/safe-file", size: 128, mtime: "2026-05-20T00:00:00Z" },
    ]);
  });

  it("keeps browsing unchanged and does not request a restore grant while listing files", async () => {
    const user = userEvent.setup();
    render(<SnapshotBrowser taskId={101} token="auth-marker" />);

    await user.click(await screen.findByRole("button", { name: /abcdef12/ }));
    expect(await screen.findByText("safe-file")).toBeInTheDocument();

    expect(apiClient.listSnapshots).toHaveBeenCalledWith("auth-marker", 101);
    expect(apiClient.listSnapshotFiles).toHaveBeenCalledWith("auth-marker", 101, "abcdef1234567890", "/");
    expect(requestSnapshotRestoreCredentialGrantMock).not.toHaveBeenCalled();
    expect(restoreSnapshotMock).not.toHaveBeenCalled();
  });

  it("requires a local-only reason before requesting a snapshot restore grant", async () => {
    const user = userEvent.setup();
    render(<SnapshotBrowser taskId={101} token="auth-marker" />);

    await user.click(await screen.findByRole("button", { name: /abcdef12/ }));
    await user.click(await screen.findByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "恢复 1 项" }));
    await user.click(await screen.findByRole("button", { name: "申请授权并恢复" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("请填写授权原因。");
    expect(requestOneTimeStepUpProofMock).not.toHaveBeenCalled();
    expect(requestSnapshotRestoreCredentialGrantMock).not.toHaveBeenCalled();
    expect(restoreSnapshotMock).not.toHaveBeenCalled();
  });

  it("requests step-up and task grant before restoring with the same proof without storing material", async () => {
    const user = userEvent.setup();
    render(<SnapshotBrowser taskId={101} token="auth-marker" />);

    await user.click(await screen.findByRole("button", { name: /abcdef12/ }));
    await user.click(await screen.findByRole("checkbox"));
    await user.clear(screen.getByLabelText("恢复目标路径"));
    await user.type(screen.getByLabelText("恢复目标路径"), "/restore-target");
    await user.click(screen.getByRole("button", { name: "恢复 1 项" }));
    expect(await screen.findByRole("dialog", { name: "需要快照恢复临时授权" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("授权原因"), "恢复误删文件");
    await user.click(screen.getByRole("button", { name: "申请授权并恢复" }));

    await waitFor(() => expect(requestSnapshotRestoreCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(requestOneTimeStepUpProofMock).toHaveBeenCalledWith({ persist: false, reuseCached: false });
    expect(apiClient.requestSnapshotRestoreCredentialGrant).toHaveBeenCalledWith(
      "auth-marker",
      { taskId: 101, reason: "恢复误删文件", requestedTtlSeconds: 600 },
      "step-up-marker",
    );
    expect(apiClient.restoreSnapshot).toHaveBeenCalledWith(
      "auth-marker",
      101,
      "abcdef1234567890",
      ["/safe-file"],
      "/restore-target",
      "step-up-marker",
    );
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "需要快照恢复临时授权" })).not.toBeInTheDocument());
    expect(toastSuccessMock).toHaveBeenCalled();

    const browserStorage = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(browserStorage).not.toContain("恢复误删文件");
    expect(browserStorage).not.toContain("CREDENTIAL_GRANT_REQUIRED");
    expect(browserStorage).not.toContain("active");
    expect(browserStorage).not.toContain("9");
    expect(browserStorage).not.toContain("/safe-file");
    expect(browserStorage).not.toContain("/restore-target");
    expect(browserStorage).not.toContain("step-up-marker");
    expect(browserStorage).not.toContain("abcdef");
  });

  it("renders grant errors as text and does not restore", async () => {
    const user = userEvent.setup();
    requestSnapshotRestoreCredentialGrantMock.mockRejectedValueOnce(new Error("需要临时授权 <script>alert(1)</script>"));
    render(<SnapshotBrowser taskId={101} token="auth-marker" />);

    await user.click(await screen.findByRole("button", { name: /abcdef12/ }));
    await user.click(await screen.findByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "恢复 1 项" }));
    await user.type(await screen.findByLabelText("授权原因"), "恢复误删文件");
    await user.click(screen.getByRole("button", { name: "申请授权并恢复" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("需要临时授权 <script>alert(1)</script>");
    expect(alert.innerHTML).not.toContain("<script>");
    expect(restoreSnapshotMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });
});
