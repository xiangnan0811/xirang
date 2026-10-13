import type { ReactElement } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { ConfigImportPreviewError } from "@/lib/api/config-import-response";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier, rememberAuthIdentity } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { ConfigImportConflict, ConfigImportPreview } from "@/types/domain";
import { ConfigExportImport } from "./config-export-import";

const {
  ensureStepUpProofMock,
  requestConfigImportCredentialGrantMock,
  requestConfigExportCredentialGrantMock,
  importConfigMock,
  previewConfigMock,
  exportConfigMock,
  toastSuccessMock,
  toastErrorMock,
  toastWarningMock,
  authState,
} = vi.hoisted(() => ({
  ensureStepUpProofMock: vi.fn(),
  requestConfigImportCredentialGrantMock: vi.fn(),
  requestConfigExportCredentialGrantMock: vi.fn(),
  importConfigMock: vi.fn(),
  previewConfigMock: vi.fn(),
  exportConfigMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
  toastWarningMock: vi.fn(),
  authState: {
    token: "auth-marker" as string | null,
    role: "admin" as "admin" | "operator" | "viewer" | null,
    totpEnabled: true,
    authTransitioning: false,
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authState.token,
    role: authState.role,
    totpEnabled: authState.totpEnabled,
    authTransitioning: authState.authTransitioning,
    ensureStepUpProof: ensureStepUpProofMock,
  }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    exportConfig: exportConfigMock,
    importConfig: importConfigMock,
    previewConfig: previewConfigMock,
    requestConfigImportCredentialGrant: requestConfigImportCredentialGrantMock,
    requestConfigExportCredentialGrant: requestConfigExportCredentialGrantMock,
  },
}));

vi.mock("sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
    warning: toastWarningMock,
  },
}));

function panel() {
  return (
    <MemoryRouter>
      <ConfigExportImport />
    </MemoryRouter>
  );
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function previewDto(conflict: ConfigImportConflict = "skip"): ConfigImportPreview {
  const counts = {
    input: 0,
    created: 0,
    updated: 0,
    skipped: 0,
    rejected: 0,
    pendingValidation: 0,
    disabledImported: 0,
  };
  return {
    conflict,
    basis: "current_snapshot",
    entities: [
      { entity: "nodes", ...counts },
      { entity: "ssh_keys", ...counts },
      { entity: "policies", ...counts },
      { entity: "tasks", ...counts },
      { entity: "system_settings", ...counts },
    ],
    warnings: [],
    pendingChecks: [],
    pendingChecksTruncated: 0,
    warningsTruncated: 0,
    systemSettings: { willWrite: false, created: 0, updated: 0, appliesInSkip: false },
    secrets: [],
    secretsTruncated: 0,
    assetGraph: [
      { entity: "backup_repositories", input: 0, status: "not_present" },
      { entity: "task_repository_links", input: 0, status: "not_present" },
      { entity: "backup_retention_policies", input: 0, status: "not_present" },
      { entity: "recovery_point_holds", input: 0, status: "not_present" },
    ],
    requiresApplyValidation: true,
  };
}

function createImportFile(data: Record<string, unknown>): File {
  const file = new File([JSON.stringify(data)], "xirang-config.json", { type: "application/json" });
  Object.defineProperty(file, "text", {
    configurable: true,
    value: vi.fn().mockResolvedValue(JSON.stringify(data)),
  });
  return file;
}

function signalOf(calls: unknown[][], callIndex = 0): AbortSignal {
  const args = calls[callIndex];
  const last = args?.[args.length - 1];
  if (!last || typeof last !== "object" || !("signal" in last) || !(last.signal instanceof AbortSignal)) {
    throw new Error("missing abort signal");
  }
  return last.signal;
}

function switchAwayAndBack(rerender: (ui: ReactElement) => void) {
  authState.role = "operator";
  rememberAuthIdentity(authState.token, "operator");
  bumpAuthSessionGeneration();
  rerender(panel());
  authState.role = "admin";
  rememberAuthIdentity(authState.token, "admin");
  bumpAuthSessionGeneration();
  rerender(panel());
}

async function continuePreview(user: UserEvent) {
  expect(await screen.findByRole("dialog", { name: "导入预览" })).toBeInTheDocument();
  const continueButton = screen.getByRole("button", { name: "确认并继续验证" });
  await waitFor(() => expect(continueButton).toBeEnabled());
  await user.click(continueButton);
}

function installPreviewMock() {
  previewConfigMock.mockImplementation((_token: string, _data: Record<string, unknown>, conflict: ConfigImportConflict = "skip") =>
    Promise.resolve(previewDto(conflict)),
  );
}

async function submitImport(user: UserEvent, file: File) {
  fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });
  await continuePreview(user);
  await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
  fireEvent.click(screen.getByRole("button", { name: "申请授权并导入" }));
}

describe("ConfigExportImport", () => {
  beforeEach(() => {
    clearAuthTransitionBarrier();
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    authState.token = "auth-marker";
    authState.role = "admin";
    authState.totpEnabled = true;
    authState.authTransitioning = false;
    rememberAuthIdentity("auth-marker", "admin");
    ensureStepUpProofMock.mockResolvedValue("step-up-marker");
    requestConfigImportCredentialGrantMock.mockResolvedValue({ id: 7, status: "active" });
    requestConfigExportCredentialGrantMock.mockResolvedValue({ id: 8, status: "active" });
    exportConfigMock.mockResolvedValue({ version: "1.0", data: {} });
    importConfigMock.mockResolvedValue({ imported: 1, skipped: 0 });
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: vi.fn(() => "blob:test"),
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: vi.fn(),
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    installPreviewMock();
  });

  it("opens a grant dialog, requests step-up and grant, then imports without storing grant material", async () => {
    const user = userEvent.setup();
    render(panel());

    const file = createImportFile({ ssh_keys: [{ name: "safe-entry" }] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });
    await continuePreview(user);

    expect(await screen.findByRole("dialog", { name: "需要配置导入临时授权" })).toBeInTheDocument();
    expect(previewConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { ssh_keys: [{ name: "safe-entry" }] },
      "skip",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    await user.type(screen.getByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));

    await waitFor(() => expect(requestConfigImportCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(
      STEP_UP_ACTIONS.configImport,
      { persist: false, reuseCached: false },
    );
    expect(apiClient.requestConfigImportCredentialGrant).toHaveBeenCalledWith(
      "auth-marker",
      { reason: "例行恢复", requestedTtlSeconds: 600 },
      "step-up-marker",
    );
    expect(apiClient.importConfig).toHaveBeenCalledWith(
      "auth-marker",
      { ssh_keys: [{ name: "safe-entry" }] },
      "skip",
      "step-up-marker",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "需要配置导入临时授权" })).not.toBeInTheDocument());
    expect(toastSuccessMock).toHaveBeenCalled();

    const browserStorage = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(browserStorage).not.toContain("例行恢复");
    expect(browserStorage).not.toContain("safe-entry");
    expect(browserStorage).not.toContain("CREDENTIAL_GRANT_REQUIRED");
    expect(browserStorage).not.toContain("active");
    expect(browserStorage).not.toContain("7");
  });

  it("opens a separate sensitive export grant dialog, requests grant, then exports with secrets without storing material", async () => {
    const user = userEvent.setup();
    render(panel());

    await user.click(screen.getByRole("button", { name: "导出含敏感字段配置" }));
    expect(await screen.findByRole("dialog", { name: "需要敏感配置导出临时授权" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("授权原因"), "例行导出");
    await user.click(screen.getByRole("button", { name: "申请授权并导出" }));

    await waitFor(() => expect(requestConfigExportCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(
      STEP_UP_ACTIONS.configExport,
      { persist: false, reuseCached: false },
    );
    expect(apiClient.requestConfigExportCredentialGrant).toHaveBeenCalledWith(
      "auth-marker",
      { reason: "例行导出", requestedTtlSeconds: 600 },
      "step-up-marker",
    );
    expect(apiClient.exportConfig).toHaveBeenCalledWith(
      "auth-marker",
      true,
      "step-up-marker",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "需要敏感配置导出临时授权" })).not.toBeInTheDocument());

    const browserStorage = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(browserStorage).not.toContain("例行导出");
    expect(browserStorage).not.toContain("CREDENTIAL_GRANT_REQUIRED");
    expect(browserStorage).not.toContain("active");
    expect(browserStorage).not.toContain("8");
    expect(browserStorage).not.toContain("version");
  });

  it("requires a local-only reason before requesting a config export grant", async () => {
    const user = userEvent.setup();
    render(panel());

    await user.click(screen.getByRole("button", { name: "导出含敏感字段配置" }));
    await user.click(await screen.findByRole("button", { name: "申请授权并导出" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("请填写授权原因。");
    expect(requestConfigExportCredentialGrantMock).not.toHaveBeenCalled();
    expect(exportConfigMock).not.toHaveBeenCalledWith("auth-marker", true, "step-up-marker");
  });

  it("renders sensitive export grant errors as text without session expiry redirect", async () => {
    const user = userEvent.setup();
    requestConfigExportCredentialGrantMock.mockRejectedValueOnce(new Error("需要临时授权 <script>alert(1)</script>"));
    render(panel());

    await user.click(screen.getByRole("button", { name: "导出含敏感字段配置" }));
    await user.type(await screen.findByLabelText("授权原因"), "例行导出");
    await user.click(screen.getByRole("button", { name: "申请授权并导出" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("需要临时授权 <script>alert(1)</script>");
    expect(alert.innerHTML).not.toContain("<script>");
    expect(window.location.pathname).not.toBe("/login");
    expect(exportConfigMock).not.toHaveBeenCalledWith("auth-marker", true, "step-up-marker");
  });

  it("sanitizes grant errors through React text rendering and does not treat them as session expiry", async () => {
    const user = userEvent.setup();
    requestConfigImportCredentialGrantMock.mockRejectedValueOnce(new Error("需要临时授权 <script>alert(1)</script>"));
    render(panel());

    const file = createImportFile({ ssh_keys: [] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });
    await continuePreview(user);
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("需要临时授权 <script>alert(1)</script>");
    expect(alert.innerHTML).not.toContain("<script>");
    expect(window.location.pathname).not.toBe("/login");
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("requires a local-only reason before requesting a config import grant", async () => {
    const user = userEvent.setup();
    render(panel());

    const file = createImportFile({ ssh_keys: [] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });
    await continuePreview(user);
    await user.click(await screen.findByRole("button", { name: "申请授权并导入" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("请填写授权原因。");
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("does not request step-up or grant when the selected import file is invalid", async () => {
    const invalidFile = new File(["[]"], "xirang-config.json", { type: "application/json" });
    Object.defineProperty(invalidFile, "text", {
      configurable: true,
      value: vi.fn().mockResolvedValue("[]"),
    });
    render(panel());

    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [invalidFile] } });

    expect(await screen.findByRole("dialog", { name: "导入预览" })).toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent("导入文件必须是 JSON 对象");
    expect(screen.getByRole("button", { name: "确认并继续验证" })).toBeDisabled();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(previewConfigMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("previews the selected file immediately and imports only the file confirmed afterward", async () => {
    const user = userEvent.setup();
    render(panel());

    const file = createImportFile({ ssh_keys: [{ name: "temporary-entry" }] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });

    expect(await screen.findByRole("dialog", { name: "导入预览" })).toBeInTheDocument();
    await waitFor(() => expect(file.text).toHaveBeenCalledTimes(1));
    expect(document.body).not.toHaveTextContent("temporary-entry");
    await user.click(screen.getByRole("button", { name: "取消" }));

    expect(screen.queryByRole("dialog", { name: "导入预览" })).not.toBeInTheDocument();
    const nextFile = createImportFile({ ssh_keys: [] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [nextFile] } });
    await continuePreview(user);
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));

    await waitFor(() => expect(importConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { ssh_keys: [] },
      "skip",
      "step-up-marker",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ));
    expect(nextFile.text).toHaveBeenCalledTimes(3);
    expect(importConfigMock).toHaveBeenCalledTimes(1);
    const stored = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(stored).not.toContain("temporary-entry");
    expect(stored).not.toContain("ssh_keys");
  });

  it("keeps ordinary config export available when two-factor authentication is disabled", async () => {
    authState.totpEnabled = false;
    const user = userEvent.setup();
    render(panel());

    await user.click(screen.getByRole("button", { name: "导出配置" }));

    await waitFor(() => expect(exportConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      false,
      undefined,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ));
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(requestConfigExportCredentialGrantMock).not.toHaveBeenCalled();
  });

  it("previews without two-factor authentication and does not import or request proof", async () => {
    authState.totpEnabled = false;
    const file = createImportFile({ ssh_keys: [{ name: "preview-only" }] });
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });

    const dialog = await screen.findByRole("dialog", { name: "导入预览" });
    await waitFor(() => expect(previewConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { ssh_keys: [{ name: "preview-only" }] },
      "skip",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ));
    await waitFor(() => expect(dialog).toHaveTextContent("此预览不会锁定配置。最终以完成的导入结果为准；配置若发生变化，结果可能不同。"));
    expect(dialog).toHaveTextContent("需先启用两步验证。实际执行仍需二次验证及适用的授权原因。");
    expect(within(dialog).getByRole("link", { name: "启用两步验证" })).toHaveAttribute("href", "/app/settings?tab=account");
    const continueButton = within(dialog).getByRole("button", { name: "确认并继续验证" });
    expect(continueButton).toBeDisabled();
    fireEvent.click(continueButton);
    expect(screen.getByRole("dialog", { name: "导入预览" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "需要配置导入临时授权" })).not.toBeInTheDocument();
    expect(file.text).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
  });

  it("does not export while authentication is transitioning", async () => {
    beginAuthTransitionBarrier();
    const user = userEvent.setup();
    render(panel());

    await user.click(screen.getByRole("button", { name: "导出配置" }));

    expect(exportConfigMock).not.toHaveBeenCalled();
  });

  it("states that the default export omits secrets and that skip still updates settings", async () => {
    const user = userEvent.setup();
    render(panel());
    const text = document.body.textContent ?? "";
    expect(text.match(/默认导出不含秘密/g)).toHaveLength(1);
    expect(text).toContain("同名实体会被跳过，系统设置仍会更新。");
    expect(text).toContain("覆盖不会替换节点密码或内嵌私钥。");
    expect(text).not.toContain("也不是完整恢复包");

    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ ssh_keys: [] })] } });
    expect(await screen.findByRole("dialog", { name: "导入预览" })).toBeInTheDocument();
    expect(screen.getByLabelText("名称已存在时")).toHaveValue("skip");
    expect(screen.getByText("同名实体会被跳过，系统设置仍会写入。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog", { name: "导入预览" })).not.toBeInTheDocument();
  });
});

describe("config import outcomes", () => {
  beforeEach(() => {
    clearAuthTransitionBarrier();
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    authState.token = "auth-marker";
    authState.role = "admin";
    authState.totpEnabled = true;
    authState.authTransitioning = false;
    rememberAuthIdentity("auth-marker", "admin");
    ensureStepUpProofMock.mockResolvedValue("step-up-marker");
    requestConfigImportCredentialGrantMock.mockResolvedValue({ id: 7, status: "active" });
    installPreviewMock();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
  });

  function expectCount(label: RegExp, value: string) {
    const matches = screen.getAllByText(label);
    expect(matches.some((node) => node.parentElement?.textContent?.includes(value))).toBe(true);
  }

  it("shows a clean success without a remediation link", async () => {
    const user = userEvent.setup();
    importConfigMock.mockResolvedValue({
      nodes: 1, sshKeys: 1, policies: 0, tasks: 0, systemSettings: 0,
      imported: 2, skipped: 1, created: 2, updated: 0, rejected: 0, disabledImported: 0,
      warnings: [], warningsTruncated: 0,
    });
    render(panel());
    await submitImport(user, createImportFile({ nodes: [] }));
    await waitFor(() => expect(document.body).toHaveTextContent(/configExport\.result\.successTitle|Configuration imported|配置已导入/));
    expectCount(/configExport\.result\.created|^Created$|^新建$/, "2");
    expectCount(/configExport\.result\.skipped|^Skipped$|^已跳过$/, "1");
    expect(screen.queryByRole("link", { name: /configExport\.remediation\.openKeys|Open SSH keys|打开 SSH 密钥/ })).not.toBeInTheDocument();
    expect(toastSuccessMock).toHaveBeenCalled();
  });

  it("shows warning codes, disabled count, and fixed remediation links without raw server text", async () => {
    const user = userEvent.setup();
    importConfigMock.mockResolvedValue({
      nodes: 1, sshKeys: 1, policies: 0, tasks: 0, systemSettings: 0,
      imported: 1, skipped: 2, created: 1, updated: 0, rejected: 0, disabledImported: 1,
      warningsTruncated: 3,
      warnings: [
        { entity: "ssh_keys", index: 0, name: "edge-key", code: "missing_private_key", message: "SECRET_WARNING_TEXT" },
        { entity: "ssh_keys", index: 2, code: "invalid_scope", message: "scope exploded" },
        { entity: "nodes", index: 1, code: "unresolved_ssh_key", message: "bind exploded" },
      ],
    });
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [{ name: "edge-key" }] }));
    await waitFor(() => expect(document.body).toHaveTextContent("未提供可用私钥，密钥保持禁用。"));
    expect(document.body).toHaveTextContent("新密钥强制禁用；覆盖项被拒绝，已有密钥保持不变。");
    expect(document.body).toHaveTextContent("节点未绑定当前系统中的 SSH 密钥。");
    expect(document.body).toHaveTextContent("edge-key");
    expect(document.body).not.toHaveTextContent("SECRET_WARNING_TEXT");
    expect(document.body).not.toHaveTextContent("scope exploded");
    expect(document.body).not.toHaveTextContent("bind exploded");
    expectCount(/configExport\.result\.disabledImported|^Imported disabled$|^导入后禁用$/, "1");
    expect(document.body).toHaveTextContent(/configExport\.result\.warningsTruncated|additional warnings|另有 3/);
    expect(document.body).toHaveTextContent(/configExport\.remediation\.enableManually|does not enable a disabled key|不会启用已禁用的密钥/);
    expect(screen.getByRole("link", { name: /configExport\.remediation\.openKeys|Open SSH keys|打开 SSH 密钥/ })).toHaveAttribute("href", "/app/ssh-keys");
    expect(screen.getByRole("link", { name: /configExport\.remediation\.openNodes|Open nodes|打开节点/ })).toHaveAttribute("href", "/app/nodes");
    const stored = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(stored).not.toContain("edge-key");
    expect(stored).not.toContain("missing_private_key");
    expect(stored).not.toContain("例行恢复");
    expect(stored).not.toContain("xirang-config.json");
    expect(importConfigMock).toHaveBeenCalledTimes(1);
  });

  async function settleNameImport(user: UserEvent, warnings: Array<Record<string, unknown>>, rejected = 1, disabledImported = 0) {
    importConfigMock.mockResolvedValue({
      nodes: 1, sshKeys: 1, policies: 0, tasks: 0, systemSettings: 0,
      imported: 1, skipped: 0, created: 0, updated: 0,
      rejected, disabledImported, warningsTruncated: 0,
      warnings,
    });
    render(panel());
    await submitImport(user, createImportFile({ nodes: [{ name: "web,1", ssh_key_name: "edge,key" }] }));
  }

  it("sends duplicate names back to the file and does not open key or node remediation", async () => {
    const user = userEvent.setup();
    await settleNameImport(user, [
      { entity: "nodes", index: 0, name: "web,1", code: "duplicate_name", message: "RAW_DUPLICATE" },
      { entity: "ssh_keys", index: 1, name: "edge,key", code: "duplicate_name", detail: "SELECT secret FROM ssh_keys" },
    ], 2);

    await waitFor(() => expect(document.body).toHaveTextContent("文件内名称重复，该项已拒绝。"));
    expect(document.body).toHaveTextContent("web,1");
    expect(document.body).toHaveTextContent("edge,key");
    expect(document.body).toHaveTextContent("请修正导入文件中的重复名称后再导入。");
    expect(document.body).not.toHaveTextContent("修复导入的密钥和节点");
    expect(document.body).not.toHaveTextContent("复核用途、目标节点或标签，以及到期时间。");
    expect(document.body).not.toHaveTextContent("手动启用密钥");
    expect(document.body).not.toHaveTextContent("编辑节点并重新绑定 SSH 密钥。");
    expect(document.body).not.toHaveTextContent("编辑密钥并补上私钥。");
    expect(document.body).not.toHaveTextContent("验证关联节点。");
    expect(document.body).not.toHaveTextContent("RAW_DUPLICATE");
    expect(document.body).not.toHaveTextContent("SELECT secret");
    expect(screen.queryByRole("link", { name: "打开 SSH 密钥" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "打开节点" })).not.toBeInTheDocument();
  });

  it.each([0, 1])("routes ssh key name references with %i disabled imports to scope review and manual enable only", async (disabledImported) => {
    const user = userEvent.setup();
    await settleNameImport(user, [
      { entity: "ssh_keys", index: 0, name: "edge,key", code: "invalid_reference", message: "RAW_REFERENCE" },
      { entity: "ssh_keys", index: 0, code: "unresolved_node_scope" },
      { entity: "ssh_keys", index: 2, code: "reference_conflict", error: "RAW_CONFLICT" },
      { entity: "ssh_keys", index: 2, code: "unresolved_node_scope" },
    ], 1, disabledImported);

    await waitFor(() => expect(document.body).toHaveTextContent("名称字段格式无效。"));
    expect(document.body).toHaveTextContent("名称清空与旧引用冲突。");
    expect(document.body).toHaveTextContent("edge,key");
    expect(document.body).toHaveTextContent("复核用途、目标节点或标签，以及到期时间。");
    expect(document.body).toHaveTextContent("手动启用密钥。密钥轮换不会启用已禁用的密钥。");
    expect(screen.getByRole("link", { name: "打开 SSH 密钥" })).toHaveAttribute("href", "/app/ssh-keys");
    expect(screen.queryByRole("link", { name: "打开节点" })).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("请修正导入文件中的重复名称后再导入。");
    expect(document.body).not.toHaveTextContent("编辑节点并重新绑定 SSH 密钥。");
    expect(document.body).not.toHaveTextContent("编辑密钥并补上私钥。");
    expect(document.body).not.toHaveTextContent("验证关联节点。");
    expect(document.body).not.toHaveTextContent("RAW_REFERENCE");
    expect(document.body).not.toHaveTextContent("RAW_CONFLICT");
  });

  it("routes node name references to the rebind entry only", async () => {
    const user = userEvent.setup();
    await settleNameImport(user, [
      { entity: "nodes", index: 1, name: "web,1", code: "invalid_reference", message: "RAW_NODE_REFERENCE" },
      { entity: "nodes", index: 1, code: "unresolved_ssh_key" },
      { entity: "nodes", index: 3, code: "reference_conflict", detail: "RAW_NODE_CONFLICT" },
      { entity: "nodes", index: 3, code: "unresolved_ssh_key" },
    ]);

    await waitFor(() => expect(document.body).toHaveTextContent("名称字段格式无效。"));
    expect(document.body).toHaveTextContent("名称清空与旧引用冲突。");
    expect(document.body).toHaveTextContent("web,1");
    expect(document.body).toHaveTextContent("编辑节点并重新绑定 SSH 密钥。");
    expect(screen.getByRole("link", { name: "打开节点" })).toHaveAttribute("href", "/app/nodes");
    expect(screen.queryByRole("link", { name: "打开 SSH 密钥" })).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("请修正导入文件中的重复名称后再导入。");
    expect(document.body).not.toHaveTextContent("复核用途、目标节点或标签，以及到期时间。");
    expect(document.body).not.toHaveTextContent("手动启用密钥");
    expect(document.body).not.toHaveTextContent("编辑密钥并补上私钥。");
    expect(document.body).not.toHaveTextContent("验证关联节点。");
    expect(document.body).not.toHaveTextContent("RAW_NODE_REFERENCE");
    expect(document.body).not.toHaveTextContent("RAW_NODE_CONFLICT");
  });

  it("keeps independent unresolved warnings on their legacy remediation path", async () => {
    const user = userEvent.setup();
    await settleNameImport(user, [
      { entity: "nodes", index: 1, code: "invalid_reference" },
      { entity: "nodes", index: 2, code: "unresolved_ssh_key" },
      { entity: "ssh_keys", index: 1, code: "unresolved_node_scope" },
    ]);

    await waitFor(() => expect(document.body).toHaveTextContent("名称字段格式无效。"));
    expect(document.body).toHaveTextContent("复核用途、目标节点或标签，以及到期时间。");
    expect(document.body).toHaveTextContent("编辑节点并重新绑定 SSH 密钥。");
    expect(document.body).toHaveTextContent("验证关联节点。");
    expect(screen.getByRole("link", { name: "打开 SSH 密钥" })).toHaveAttribute("href", "/app/ssh-keys");
    expect(screen.getByRole("link", { name: "打开节点" })).toHaveAttribute("href", "/app/nodes");
  });

  it("keeps a non-node non-key name reference on the warning line only", async () => {
    const user = userEvent.setup();
    await settleNameImport(user, [
      { entity: "policies", index: 0, code: "invalid_reference", message: "RAW_POLICY" },
    ]);

    await waitFor(() => expect(document.body).toHaveTextContent("名称字段格式无效。"));
    expect(document.body).not.toHaveTextContent("修复导入的密钥和节点");
    expect(document.body).not.toHaveTextContent("请修正导入文件中的重复名称后再导入。");
    expect(document.body).not.toHaveTextContent("RAW_POLICY");
    expect(screen.queryByRole("link", { name: "打开 SSH 密钥" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "打开节点" })).not.toBeInTheDocument();
  });

  it("shows file correction together with the entity route for each name warning", async () => {
    const user = userEvent.setup();
    await settleNameImport(user, [
      { entity: "nodes", index: 0, name: "web,1", code: "duplicate_name", message: "RAW_DUPLICATE" },
      { entity: "ssh_keys", index: 1, code: "invalid_reference", message: "RAW_REFERENCE" },
      { entity: "nodes", index: 2, code: "reference_conflict", message: "RAW_CONFLICT" },
    ], 3);

    await waitFor(() => expect(document.body).toHaveTextContent("文件内名称重复，该项已拒绝。"));
    expect(document.body).toHaveTextContent("名称字段格式无效。");
    expect(document.body).toHaveTextContent("名称清空与旧引用冲突。");
    expect(document.body).toHaveTextContent("请修正导入文件中的重复名称后再导入。");
    expect(document.body).toHaveTextContent("复核用途、目标节点或标签，以及到期时间。");
    expect(document.body).toHaveTextContent("手动启用密钥。密钥轮换不会启用已禁用的密钥。");
    expect(document.body).toHaveTextContent("编辑节点并重新绑定 SSH 密钥。");
    expect(screen.getByRole("link", { name: "打开 SSH 密钥" })).toHaveAttribute("href", "/app/ssh-keys");
    expect(screen.getByRole("link", { name: "打开节点" })).toHaveAttribute("href", "/app/nodes");
    expect(document.body).not.toHaveTextContent("编辑密钥并补上私钥。");
    expect(document.body).not.toHaveTextContent("验证关联节点。");
    expect(document.body).not.toHaveTextContent("RAW_DUPLICATE");
    expect(document.body).not.toHaveTextContent("RAW_REFERENCE");
    expect(document.body).not.toHaveTextContent("RAW_CONFLICT");
  });

  it("shows a rejected count without offering another submission", async () => {
    const user = userEvent.setup();
    importConfigMock.mockResolvedValue({
      nodes: 0, sshKeys: 0, policies: 0, tasks: 0, systemSettings: 0,
      imported: 1, skipped: 0, created: 0, updated: 1, rejected: 4, disabledImported: 2,
      warningsTruncated: 0,
      warnings: [{ entity: "ssh_keys", index: 4, code: "invalid_private_key" }],
    });
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(document.body).toHaveTextContent(/configExport\.result\.rejectedTitle|Some imported items were rejected|部分导入项被拒绝/));
    expectCount(/configExport\.result\.rejected|^Rejected$|^已拒绝$/, "4");
    expectCount(/configExport\.result\.disabledImported|^Imported disabled$|^导入后禁用$/, "2");
    expect(screen.queryByRole("button", { name: /^(重试|Retry)$/ })).not.toBeInTheDocument();
    expect(importConfigMock).toHaveBeenCalledTimes(1);
  });

  it("shows a definite import failure without the raw server body or a retry", async () => {
    const user = userEvent.setup();
    importConfigMock.mockRejectedValueOnce(new ApiError(400, "bad payload <script>alert(1)</script>"));
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(document.body).toHaveTextContent(/configExport\.result\.failureTitle|Import failed|导入失败/));
    expect(document.body).not.toHaveTextContent("bad payload");
    expect(document.body.innerHTML).not.toContain("<script>");
    expect(importConfigMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: /^(重试|Retry)$/ })).not.toBeInTheDocument();
  });

  it("shows an unknown result after a network failure and does not submit again", async () => {
    const user = userEvent.setup();
    importConfigMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(document.body).toHaveTextContent(/configExport\.result\.unknownTitle|Import result unknown|导入结果未知/));
    expect(document.body).toHaveTextContent(/configExport\.result\.unknownBody|will not submit it again|不会再次提交/);
    expect(screen.queryByText(/Failed to fetch/)).not.toBeInTheDocument();
    expect(importConfigMock).toHaveBeenCalledTimes(1);
  });

  it("treats an import 500 as unknown rather than a clean failure", async () => {
    const user = userEvent.setup();
    importConfigMock.mockRejectedValueOnce(new ApiError(500, "database exploded"));
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(document.body).toHaveTextContent(/configExport\.result\.unknownTitle|Import result unknown|导入结果未知/));
    expect(screen.queryByText(/database exploded/)).not.toBeInTheDocument();
    expect(importConfigMock).toHaveBeenCalledTimes(1);
  });
});

describe("stale config operation boundaries", () => {
  beforeEach(() => {
    clearAuthTransitionBarrier();
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    authState.token = "auth-marker";
    authState.role = "admin";
    authState.totpEnabled = true;
    authState.authTransitioning = false;
    rememberAuthIdentity("auth-marker", "admin");
    ensureStepUpProofMock.mockResolvedValue("step-up-marker");
    requestConfigImportCredentialGrantMock.mockResolvedValue({ id: 7, status: "active" });
    requestConfigExportCredentialGrantMock.mockResolvedValue({ id: 8, status: "active" });
    exportConfigMock.mockResolvedValue({ version: "1.0", data: {} });
    importConfigMock.mockResolvedValue({ imported: 1, skipped: 0 });
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: vi.fn(() => "blob:test"),
    });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    installPreviewMock();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
  });

  it("does not open a grant or step-up challenge after preview resolves for a replaced admin session", async () => {
    const pending = deferred<ConfigImportPreview>();
    previewConfigMock.mockReturnValueOnce(pending.promise);
    const view = render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ ssh_keys: [] })] } });
    await waitFor(() => expect(previewConfigMock).toHaveBeenCalledTimes(1));
    switchAwayAndBack(view.rerender);
    await act(async () => {
      pending.resolve(previewDto());
    });
    expect(screen.queryByRole("dialog", { name: "需要配置导入临时授权" })).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "导入预览" })).not.toBeInTheDocument();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastWarningMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("does not request step-up after the import file is read for a replaced session", async () => {
    const user = userEvent.setup();
    const body = deferred<string>();
    let reads = 0;
    const file = new File(["{}"], "xirang-config.json", { type: "application/json" });
    Object.defineProperty(file, "text", {
      configurable: true,
      value: vi.fn(() => {
        reads += 1;
        if (reads === 1) return Promise.resolve(JSON.stringify({ ssh_keys: [] }));
        return body.promise;
      }),
    });
    const view = render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });
    await continuePreview(user);
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    fireEvent.click(screen.getByRole("button", { name: "申请授权并导入" }));
    await waitFor(() => expect(file.text).toHaveBeenCalledTimes(2));
    switchAwayAndBack(view.rerender);
    await act(async () => {
      body.resolve(JSON.stringify({ ssh_keys: [] }));
    });
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("does not continue an import or open another challenge when step-up resolves after A-B-A", async () => {
    const user = userEvent.setup();
    const proof = deferred<string>();
    ensureStepUpProofMock.mockReturnValue(proof.promise);
    const view = render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1));
    switchAwayAndBack(view.rerender);
    await act(async () => {
      proof.resolve("late-proof");
    });
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not import after a grant resolves for a replaced session", async () => {
    const user = userEvent.setup();
    const grant = deferred<{ id: number; status: string }>();
    requestConfigImportCredentialGrantMock.mockReturnValueOnce(grant.promise);
    const view = render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(requestConfigImportCredentialGrantMock).toHaveBeenCalledTimes(1));
    switchAwayAndBack(view.rerender);
    await act(async () => {
      grant.resolve({ id: 7, status: "active" });
    });
    expect(importConfigMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
  });

  it("aborts an in-flight import and ignores its late success after A-B-A", async () => {
    const user = userEvent.setup();
    const pending = deferred<{ imported: number; skipped: number }>();
    importConfigMock.mockReturnValueOnce(pending.promise);
    const view = render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(importConfigMock).toHaveBeenCalledTimes(1));
    const signal = signalOf(importConfigMock.mock.calls);
    switchAwayAndBack(view.rerender);
    expect(signal.aborted).toBe(true);
    await act(async () => {
      pending.resolve({ imported: 9, skipped: 0 });
    });
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastWarningMock).not.toHaveBeenCalled();
    expect(importConfigMock).toHaveBeenCalledTimes(1);
    expect(document.body).not.toHaveTextContent(/configExport\.result\.successTitle|Configuration imported|配置已导入/);
  });

  it("reports an unknown result when the session generation changes after the import was sent", async () => {
    const user = userEvent.setup();
    const pending = deferred<{ imported: number; skipped: number }>();
    importConfigMock.mockReturnValueOnce(pending.promise);
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(importConfigMock).toHaveBeenCalledTimes(1));
    bumpAuthSessionGeneration();
    await act(async () => {
      pending.resolve({ imported: 9, skipped: 0 });
    });
    expect(toastSuccessMock).not.toHaveBeenCalled();
    await waitFor(() => expect(document.body).toHaveTextContent(/configExport\.result\.unknownTitle|Import result unknown|导入结果未知/));
    expect(importConfigMock).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "导入配置" })).toBeEnabled();
  });

  it("does not download or toast when ordinary export resolves after the session is replaced", async () => {
    const pending = deferred<{ version: string }>();
    exportConfigMock.mockReturnValueOnce(pending.promise);
    const view = render(panel());
    fireEvent.click(screen.getByRole("button", { name: "导出配置" }));
    await waitFor(() => expect(exportConfigMock).toHaveBeenCalledTimes(1));
    const signal = signalOf(exportConfigMock.mock.calls);
    switchAwayAndBack(view.rerender);
    expect(signal.aborted).toBe(true);
    await act(async () => {
      pending.resolve({ version: "1.0" });
    });
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    expect(HTMLAnchorElement.prototype.click).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  it("does not export secrets when the sensitive grant resolves after A-B-A", async () => {
    const user = userEvent.setup();
    const grant = deferred<{ id: number; status: string }>();
    requestConfigExportCredentialGrantMock.mockReturnValueOnce(grant.promise);
    const view = render(panel());
    await user.click(screen.getByRole("button", { name: "导出含敏感字段配置" }));
    await user.type(await screen.findByLabelText("授权原因"), "例行导出");
    fireEvent.click(screen.getByRole("button", { name: "申请授权并导出" }));
    await waitFor(() => expect(requestConfigExportCredentialGrantMock).toHaveBeenCalledTimes(1));
    switchAwayAndBack(view.rerender);
    await act(async () => {
      grant.resolve({ id: 8, status: "active" });
    });
    expect(exportConfigMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(URL.createObjectURL).not.toHaveBeenCalled();
  });
});

describe("config import preview", () => {
  beforeEach(() => {
    clearAuthTransitionBarrier();
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    authState.token = "auth-marker";
    authState.role = "admin";
    authState.totpEnabled = true;
    authState.authTransitioning = false;
    rememberAuthIdentity("auth-marker", "admin");
    ensureStepUpProofMock.mockResolvedValue("step-up-marker");
    requestConfigImportCredentialGrantMock.mockResolvedValue({ id: 7, status: "active" });
    importConfigMock.mockResolvedValue({ imported: 1, skipped: 0 });
    installPreviewMock();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
  });

  it("shows every entity count, separate pending checks, settings, secrets, and asset limits", async () => {
    const preview = previewDto("skip");
    preview.entities = [
      { entity: "nodes", input: 2, created: 1, updated: 1, skipped: 0, rejected: 1, pendingValidation: 1, disabledImported: 0 },
      { entity: "ssh_keys", input: 1, created: 0, updated: 0, skipped: 1, rejected: 0, pendingValidation: 0, disabledImported: 1 },
      { entity: "policies", input: 0, created: 0, updated: 0, skipped: 0, rejected: 0, pendingValidation: 0, disabledImported: 0 },
      { entity: "tasks", input: 0, created: 0, updated: 0, skipped: 0, rejected: 0, pendingValidation: 0, disabledImported: 0 },
      { entity: "system_settings", input: 1, created: 1, updated: 1, skipped: 0, rejected: 0, pendingValidation: 0, disabledImported: 0 },
    ];
    preview.systemSettings = { willWrite: true, created: 1, updated: 1, appliesInSkip: true };
    preview.warnings = [{ entity: "nodes", index: 0, name: "edge-node", code: "duplicate_name" }];
    preview.warningsTruncated = 2;
    preview.pendingChecks = [{ entity: "nodes", index: 1, code: "retained_secret" }];
    preview.pendingChecksTruncated = 4;
    preview.secrets = [{ entity: "nodes", index: 1, field: "password", present: true, action: "preserved" }];
    preview.secretsTruncated = 3;
    preview.assetGraph = [
      { entity: "backup_repositories", input: 2, status: "unknown" },
      { entity: "task_repository_links", input: 0, status: "not_present" },
      { entity: "backup_retention_policies", input: 1, status: "unsupported" },
      { entity: "recovery_point_holds", input: 0, status: "not_present" },
    ];
    previewConfigMock.mockResolvedValueOnce(preview);
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ nodes: [] })] } });

    expect(await screen.findByRole("dialog", { name: "导入预览" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "确认并继续验证" })).toBeEnabled());
    const dialog = screen.getByRole("dialog", { name: "导入预览" });
    expect(dialog).toHaveTextContent("节点");
    expect(dialog).toHaveTextContent("SSH 密钥");
    expect(dialog).toHaveTextContent("策略");
    expect(dialog).toHaveTextContent("任务");
    expect(dialog).toHaveTextContent("系统设置");
    expect(dialog).toHaveTextContent("提交时校验");
    expect(dialog).toHaveTextContent("文件中的系统设置会被写入，跳过已有实体时也一样。写入不表示每个值都会变化。");
    expect(dialog).toHaveTextContent("将新建的设置：1。将写入的设置：1。");
    expect(dialog).toHaveTextContent("即使跳过同名实体，系统设置仍会写入。");
    expect(dialog).toHaveTextContent("edge-node");
    expect(dialog).toHaveTextContent("文件内名称重复，该项已拒绝。");
    expect(dialog).toHaveTextContent("另有 2 条警告未显示。");
    expect(dialog).toHaveTextContent("已有秘密只在导入执行时检查。");
    expect(dialog).toHaveTextContent("另有 4 项检查未显示。");
    expect(dialog).toHaveTextContent("密码");
    expect(dialog).toHaveTextContent("保留已有值");
    expect(dialog).toHaveTextContent("另有 3 个秘密字段未显示。");
    expect(dialog).toHaveTextContent("备份仓库");
    expect(dialog).toHaveTextContent("未预测");
    expect(dialog).toHaveTextContent("此预览不预测备份资产的本地身份冲突、绑定或写入计数。");
    expect(dialog).toHaveTextContent("备份保留策略");
    expect(dialog).toHaveTextContent("此文件版本不导入");
    expect(dialog).toHaveTextContent("任务仓库关联");
    expect(dialog).toHaveTextContent("文件中没有");
    expect(dialog).toHaveTextContent("此预览不会锁定配置。最终以完成的导入结果为准；配置若发生变化，结果可能不同。");
    expect(dialog).toHaveTextContent("计数是当前快照上的预计值。");
    expect(dialog).not.toHaveTextContent("duplicate_name");
    expect(dialog).not.toHaveTextContent("retained_secret");
    const [preserved] = within(within(dialog).getByRole("list", { name: "此文件中的秘密字段" })).getAllByRole("listitem");
    expect(preserved).toHaveTextContent("输入是否提供: 已提供");
    expect(preserved).toHaveTextContent("凭据结果: 保留已有值");
    expect(preserved).not.toHaveTextContent("未提供");
    expect(preserved).not.toHaveTextContent("有效凭据缺失");
    expect(preserved).not.toHaveTextContent("文件中没有");
  });

  it("separates provided input from preserved, missing, skipped, and rejected credential outcomes", async () => {
    const preview = previewDto("skip");
    preview.secrets = [
      { entity: "nodes", index: 0, field: "password", present: true, action: "preserved" },
      { entity: "nodes", index: 1, field: "private_key", present: true, action: "missing" },
      { entity: "ssh_keys", index: 0, field: "private_key", present: true, action: "skipped" },
      { entity: "nodes", index: 2, field: "password", present: true, action: "rejected" },
      { entity: "ssh_keys", index: 1, field: "private_key", present: false, action: "missing" },
    ];
    previewConfigMock.mockResolvedValueOnce(preview);
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ nodes: [] })] } });

    const dialog = await screen.findByRole("dialog", { name: "导入预览" });
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "确认并继续验证" })).toBeEnabled());
    const rows = within(within(dialog).getByRole("list", { name: "此文件中的秘密字段" })).getAllByRole("listitem");
    expect(rows).toHaveLength(5);
    expect(rows[0]).toHaveTextContent("密码");
    expect(rows[0]).toHaveTextContent("输入是否提供: 已提供");
    expect(rows[0]).toHaveTextContent("凭据结果: 保留已有值");
    expect(rows[0]).not.toHaveTextContent("未提供");
    expect(rows[0]).not.toHaveTextContent("文件中没有");
    expect(rows[1]).toHaveTextContent("私钥");
    expect(rows[1]).toHaveTextContent("输入是否提供: 已提供");
    expect(rows[1]).toHaveTextContent("凭据结果: 有效凭据缺失");
    expect(rows[1]).not.toHaveTextContent("未提供");
    expect(rows[1]).not.toHaveTextContent("文件中没有");
    expect(rows[1]).not.toHaveTextContent("此文件未提供");
    expect(rows[2]).toHaveTextContent("输入是否提供: 已提供");
    expect(rows[2]).toHaveTextContent("凭据结果: 随已有实体跳过");
    expect(rows[2]).not.toHaveTextContent("有效凭据缺失");
    expect(rows[3]).toHaveTextContent("输入是否提供: 已提供");
    expect(rows[3]).toHaveTextContent("凭据结果: 随该项拒绝");
    expect(rows[3]).not.toHaveTextContent("有效凭据缺失");
    expect(rows[4]).toHaveTextContent("输入是否提供: 未提供");
    expect(rows[4]).toHaveTextContent("凭据结果: 有效凭据缺失");
    expect(rows[4]).not.toHaveTextContent("文件中没有");
    expect(rows[4]).not.toHaveTextContent("已提供");
  });

  it("does not read or preview a file larger than 10 MiB", async () => {
    const text = vi.fn();
    const file = new File(["{}"], "too-big.json", { type: "application/json" });
    Object.defineProperty(file, "size", { value: 10 * 1024 * 1024 + 1 });
    Object.defineProperty(file, "text", { configurable: true, value: text });
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });

    expect(await screen.findByRole("alert")).toHaveTextContent("导入文件不能超过 10 MiB。");
    expect(screen.getByRole("button", { name: "确认并继续验证" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "重新预览" })).not.toBeInTheDocument();
    expect(text).not.toHaveBeenCalled();
    expect(previewConfigMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("never becomes ready when the preview response is unusable", async () => {
    previewConfigMock.mockRejectedValueOnce(new ConfigImportPreviewError());
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ nodes: [] })] } });

    expect(await screen.findByRole("alert")).toHaveTextContent("无法显示预览响应。没有执行导入。");
    expect(screen.getByRole("button", { name: "确认并继续验证" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "重新预览" })).toBeInTheDocument();
    expect(screen.queryByText("此预览不会锁定配置。最终以完成的导入结果为准；配置若发生变化，结果可能不同。")).not.toBeInTheDocument();
    expect(importConfigMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
  });

  it("never becomes ready when the preview payload is malformed", async () => {
    previewConfigMock.mockResolvedValueOnce({ conflict: "overwrite", basis: "stale", entities: [] });
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ nodes: [] })] } });

    expect(await screen.findByRole("alert")).toHaveTextContent("无法显示预览响应。没有执行导入。");
    expect(screen.getByRole("button", { name: "确认并继续验证" })).toBeDisabled();
    expect(screen.queryByText("计数是当前快照上的预计值。")).not.toBeInTheDocument();
    expect(importConfigMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
  });

  it("ignores a slow first file after a second file is selected", async () => {
    const user = userEvent.setup();
    const slow = deferred<string>();
    const first = new File(["{}"], "first.json", { type: "application/json" });
    Object.defineProperty(first, "text", { configurable: true, value: vi.fn(() => slow.promise) });
    const second = createImportFile({ nodes: [{ name: "second-node" }] });
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [first] } });
    await waitFor(() => expect(first.text).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [second] } });
    await waitFor(() => expect(previewConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { nodes: [{ name: "second-node" }] },
      "skip",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ));
    const dialog = screen.getByRole("dialog", { name: "导入预览" });
    expect(dialog).toHaveTextContent("xirang-config.json");
    expect(dialog).not.toHaveTextContent("first.json");
    await act(async () => {
      slow.resolve(JSON.stringify({ nodes: [{ name: "first-node" }] }));
    });
    expect(previewConfigMock).toHaveBeenCalledTimes(1);
    expect(dialog).not.toHaveTextContent("first.json");
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(toastWarningMock).not.toHaveBeenCalled();
    await continuePreview(user);
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));
    await waitFor(() => expect(importConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { nodes: [{ name: "second-node" }] },
      "skip",
      "step-up-marker",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ));
  });

  it("keeps a late skip preview from replacing the overwrite preview", async () => {
    const user = userEvent.setup();
    const skipPreview = deferred<ConfigImportPreview>();
    previewConfigMock.mockImplementation((_token: string, _data: unknown, conflict: ConfigImportConflict = "skip") => {
      if (conflict === "overwrite") return Promise.resolve(previewDto("overwrite"));
      return skipPreview.promise;
    });
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ nodes: [{ name: "edge" }] })] } });
    await waitFor(() => expect(previewConfigMock).toHaveBeenCalledTimes(1));
    await user.selectOptions(screen.getByLabelText("名称已存在时"), "overwrite");
    await waitFor(() => expect(screen.getByRole("button", { name: "确认并继续验证" })).toBeEnabled());
    await act(async () => {
      skipPreview.resolve(previewDto("skip"));
    });
    expect(screen.getByLabelText("名称已存在时")).toHaveValue("overwrite");
    expect(previewConfigMock).toHaveBeenLastCalledWith(
      "auth-marker",
      { nodes: [{ name: "edge" }] },
      "overwrite",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    await user.click(screen.getByRole("button", { name: "确认并继续验证" }));
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));
    await waitFor(() => expect(importConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { nodes: [{ name: "edge" }] },
      "overwrite",
      "step-up-marker",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ));
    expect(importConfigMock).toHaveBeenCalledTimes(1);
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("ignores a late preview after the dialog is closed", async () => {
    const user = userEvent.setup();
    const pending = deferred<ConfigImportPreview>();
    previewConfigMock.mockReturnValueOnce(pending.promise);
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ ssh_keys: [] })] } });
    await waitFor(() => expect(previewConfigMock).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog", { name: "导入预览" })).not.toBeInTheDocument();
    await act(async () => {
      pending.resolve(previewDto());
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastWarningMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("does not replace an unknown import result when a later preview succeeds", async () => {
    const user = userEvent.setup();
    importConfigMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [] }));
    await waitFor(() => expect(document.body).toHaveTextContent("导入结果未知"));
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ nodes: [{ name: "later" }] })] } });
    await waitFor(() => expect(screen.getByRole("button", { name: "确认并继续验证" })).toBeEnabled());
    expect(document.body).toHaveTextContent("导入结果未知");
    expect(document.body).toHaveTextContent("此预览不会锁定配置。最终以完成的导入结果为准；配置若发生变化，结果可能不同。");
    expect(importConfigMock).toHaveBeenCalledTimes(1);
  });

  it("does not replace the captured file or conflict while an import is in flight", async () => {
    const pending = deferred<{ imported: number; skipped: number }>();
    importConfigMock.mockReturnValueOnce(pending.promise);
    const user = userEvent.setup();
    render(panel());
    await submitImport(user, createImportFile({ ssh_keys: [{ name: "kept" }] }));
    await waitFor(() => expect(importConfigMock).toHaveBeenCalledTimes(1));
    expect(screen.getByLabelText("导入配置")).toBeDisabled();
    expect(screen.queryByLabelText("名称已存在时")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ nodes: [{ name: "replacement" }] })] } });
    expect(previewConfigMock).toHaveBeenCalledTimes(1);
    await act(async () => {
      pending.resolve({ imported: 1, skipped: 0 });
    });
    expect(importConfigMock).toHaveBeenCalledTimes(1);
    expect(importConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { ssh_keys: [{ name: "kept" }] },
      "skip",
      "step-up-marker",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
  });
});
