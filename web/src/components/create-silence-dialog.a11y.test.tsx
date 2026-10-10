import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { CreateSilenceDialog } from "@/components/create-silence-dialog";
import { AuthProvider } from "@/context/auth-context";
import type { QuickSilenceMatch } from "@/lib/alert-silence-match";
import { apiClient } from "@/lib/api/client";
import {
  clearAuthTransitionBarrier,
  getAuthIdentitySnapshot,
  isAuthTransitionActive,
  rememberAuthIdentity,
} from "@/lib/api/core";
import { runAxe } from "@/test/a11y-helpers";
import type { NodeRecord } from "@/types/domain";

const ADMIN_TOKEN = "silence-a11y-admin";
const QUICK_MATCH: QuickSilenceMatch = { nodeId: 42, category: "XR-EXEC" };

function nodeRecord(id: number, name: string): NodeRecord {
  return {
    id,
    name,
    host: "10.0.0.8",
    address: "10.0.0.8",
    ip: "10.0.0.8",
    port: 22,
    username: "backup",
    authType: "key",
    status: "online",
    tags: [],
    lastSeenAt: "",
    lastBackupAt: "",
  };
}

function renderDialog(initialMatch?: QuickSilenceMatch) {
  return render(
    <MemoryRouter>
      <AuthProvider>
        <CreateSilenceDialog
          open
          onOpenChange={vi.fn()}
          onCreated={vi.fn()}
          token={ADMIN_TOKEN}
          initialMatch={initialMatch}
        />
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("CreateSilenceDialog a11y", () => {
  beforeEach(() => {
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    sessionStorage.setItem("xirang-auth-token", ADMIN_TOKEN);
    sessionStorage.setItem("xirang-role", "admin");
    sessionStorage.setItem("xirang-username", "axe-admin");
    vi.spyOn(apiClient, "getNode").mockResolvedValue(nodeRecord(42, "backup-a"));
    vi.spyOn(apiClient, "getNodes").mockResolvedValue([
      nodeRecord(1, "node-1"),
      nodeRecord(2, "node-2"),
    ]);
    vi.spyOn(apiClient, "createSilence").mockRejectedValue(new Error("create should not run"));
  });

  afterEach(() => {
    cleanup();
    sessionStorage.removeItem("xirang-auth-token");
    sessionStorage.removeItem("xirang-role");
    sessionStorage.removeItem("xirang-username");
    sessionStorage.removeItem("xirang-user-id");
    sessionStorage.removeItem("xirang-totp-enabled");
    rememberAuthIdentity(null, null);
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    vi.restoreAllMocks();
  });

  it("keeps a ready quick range named, keyboard reachable, and axe-clean", async () => {
    const user = userEvent.setup();
    renderDialog(QUICK_MATCH);

    const dialog = await screen.findByRole("dialog", { name: /新建静默规则|New Silence Rule/ });
    expect(getAuthIdentitySnapshot()).toEqual({ token: ADMIN_TOKEN, role: "admin" });
    await waitFor(() => {
      expect(apiClient.getNode).toHaveBeenCalledWith(
        ADMIN_TOKEN,
        42,
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
    });
    expect(await screen.findByText("backup-a")).toBeInTheDocument();
    expect(dialog).toHaveAccessibleDescription(/此节点的这一类告警都会静音|not only the current alert/);
    expect(screen.getByRole("link", { name: /查看静音规则|View silence rules/ })).toHaveAttribute(
      "href",
      "/app/settings?tab=silences",
    );
    const name = screen.getByRole("textbox", { name: /^名称$|^Name$/ });
    expect(name).toBeEnabled();
    expect(screen.getByRole("button", { name: /^创建$|^Create$/ })).toBeEnabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();

    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    const rules = screen.getByRole("link", { name: /查看静音规则|View silence rules/ });
    rules.focus();
    await user.tab();
    expect(name).toHaveFocus();
  });

  it("keeps the node check loading state named and axe-clean", async () => {
    vi.mocked(apiClient.getNode).mockImplementation(() => new Promise<NodeRecord>(() => {}));
    renderDialog(QUICK_MATCH);

    const dialog = await screen.findByRole("dialog", { name: /新建静默规则|New Silence Rule/ });
    expect(getAuthIdentitySnapshot()).toEqual({ token: ADMIN_TOKEN, role: "admin" });
    expect(await screen.findByRole("status")).toHaveTextContent(/正在核对节点|Checking this node/);
    await waitFor(() => expect(apiClient.getNode).toHaveBeenCalledTimes(1));
    expect(apiClient.getNode).toHaveBeenCalledWith(
      ADMIN_TOKEN,
      42,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(screen.queryByRole("textbox", { name: /^名称$|^Name$/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /核对完成后才能创建|Available after the node check/ })).toBeDisabled();
    expect(dialog).toHaveAccessibleDescription(/此节点的这一类告警都会静音|not only the current alert/);
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("keeps a failed node check alert and retry reachable and axe-clean", async () => {
    const user = userEvent.setup();
    vi.mocked(apiClient.getNode).mockRejectedValue(new Error("node down"));
    renderDialog(QUICK_MATCH);

    const dialog = await screen.findByRole("dialog", { name: /新建静默规则|New Silence Rule/ });
    expect(getAuthIdentitySnapshot()).toEqual({ token: ADMIN_TOKEN, role: "admin" });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/无法核对这个节点|could not be checked/);
    const retry = screen.getByRole("button", { name: /重新核对|Check again/ });
    expect(retry).toBeEnabled();
    expect(screen.queryByRole("textbox", { name: /^名称$|^Name$/ })).not.toBeInTheDocument();
    expect(screen.queryByText("backup-a")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /核对完成后才能创建|Available after the node check/ })).toBeDisabled();
    expect(apiClient.createSilence).not.toHaveBeenCalled();
    expect(await runAxe(document.body)).toHaveNoViolations();

    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    const rules = screen.getByRole("link", { name: /查看静音规则|View silence rules/ });
    rules.focus();
    await user.tab();
    expect(retry).toHaveFocus();
  });

  it("keeps the settings create dialog named and axe-clean", async () => {
    renderDialog();

    const dialog = await screen.findByRole("dialog", { name: /新建静默规则|New Silence Rule/ });
    expect(getAuthIdentitySnapshot()).toEqual({ token: ADMIN_TOKEN, role: "admin" });
    await waitFor(() => {
      expect(apiClient.getNodes).toHaveBeenCalledWith(
        ADMIN_TOKEN,
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
    });
    expect(apiClient.getNode).not.toHaveBeenCalled();
    expect(await screen.findByRole("option", { name: "node-1" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "node-2" })).toBeInTheDocument();
    expect(dialog).toHaveAccessibleDescription(/创建静默窗口|matching alerts stay quiet/);
    expect(screen.getByRole("textbox", { name: /^名称$|^Name$/ })).toBeEnabled();
    expect(screen.getByRole("combobox", { name: /节点|Node/ })).toBeEnabled();
    expect(screen.getByRole("combobox", { name: /告警类型|Alert type/ })).toBeEnabled();
    expect(screen.getByLabelText(/^开始$|^Start$/)).toBeEnabled();
    expect(screen.getByLabelText(/^结束$|^End$/)).toBeEnabled();
    expect(screen.getByRole("textbox", { name: /^备注$|^Note$/ })).toBeEnabled();
    expect(screen.getByPlaceholderText(/输入标签后按 Enter 或点击添加|Type a tag then press Enter or click Add/)).toBeEnabled();
    expect(screen.getByRole("button", { name: /1 小时|1 hour/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /^创建$|^Create$/ })).toBeEnabled();
    expect(screen.queryByRole("link", { name: /查看静音规则|View silence rules/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });
});
