import type { ReactElement } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier, rememberAuthIdentity } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { ConfigExportImport } from "./config-export-import";

const {
  ensureStepUpProofMock,
  requestConfigImportCredentialGrantMock,
  requestConfigExportCredentialGrantMock,
  importConfigMock,
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

const confirmMock = vi.fn().mockResolvedValue(true);
vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
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

async function submitImport(user: UserEvent, file: File) {
  fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });
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
    confirmMock.mockResolvedValue(true);
  });

  it("opens a grant dialog, requests step-up and grant, then imports without storing grant material", async () => {
    const user = userEvent.setup();
    render(panel());

    const file = createImportFile({ ssh_keys: [{ name: "safe-entry" }] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });

    expect(await screen.findByRole("dialog", { name: "需要配置导入临时授权" })).toBeInTheDocument();
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
    await user.click(await screen.findByRole("button", { name: "申请授权并导入" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("请填写授权原因。");
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("does not request step-up or grant when the selected import file is invalid", async () => {
    const user = userEvent.setup();
    const invalidFile = new File(["[]"], "xirang-config.json", { type: "application/json" });
    Object.defineProperty(invalidFile, "text", {
      configurable: true,
      value: vi.fn().mockResolvedValue("[]"),
    });
    render(panel());

    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [invalidFile] } });
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("导入文件必须是 JSON 对象");
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
  });

  it("does not parse or retain the import payload until grant submission", async () => {
    const user = userEvent.setup();
    render(panel());

    const file = createImportFile({ ssh_keys: [{ name: "temporary-entry" }] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });

    expect(await screen.findByRole("dialog", { name: "需要配置导入临时授权" })).toBeInTheDocument();
    expect(file.text).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "取消" }));

    expect(screen.queryByRole("dialog", { name: "需要配置导入临时授权" })).not.toBeInTheDocument();
    const nextFile = createImportFile({ ssh_keys: [] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [nextFile] } });
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));

    await waitFor(() => expect(importConfigMock).toHaveBeenCalledWith(
      "auth-marker",
      { ssh_keys: [] },
      "skip",
      "step-up-marker",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ));
    expect(nextFile.text).toHaveBeenCalledTimes(2);
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

  it("does not import or request proof when two-factor authentication is disabled", async () => {
    authState.totpEnabled = false;
    render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ ssh_keys: [] })] } });

    expect(confirmMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(importConfigMock).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: /stepUp.enableTOTP|启用两步验证|Enable two-factor/ })).toHaveAttribute(
      "href",
      "/app/settings?tab=account",
    );
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
    expect(confirmMock).toHaveBeenCalledWith({
      title: "确定导入配置吗？同名实体将跳过，系统设置仍会更新。导入的节点密码和内嵌私钥不会替换已有节点凭据。",
      description: "此操作不可撤销。",
    });
    await user.click(await screen.findByRole("button", { name: "取消" }));
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
    confirmMock.mockResolvedValue(true);
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
    confirmMock.mockResolvedValue(true);
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
  });

  it("does not open a grant or step-up challenge after confirm resolves for a replaced admin session", async () => {
    const decision = deferred<boolean>();
    confirmMock.mockReturnValueOnce(decision.promise);
    const view = render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [createImportFile({ ssh_keys: [] })] } });
    expect(confirmMock).toHaveBeenCalledTimes(1);
    switchAwayAndBack(view.rerender);
    await act(async () => {
      decision.resolve(true);
    });
    expect(screen.queryByRole("dialog", { name: "需要配置导入临时授权" })).not.toBeInTheDocument();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
  });

  it("does not request step-up after the import file is read for a replaced session", async () => {
    const user = userEvent.setup();
    const body = deferred<string>();
    const file = new File(["{}"], "xirang-config.json", { type: "application/json" });
    Object.defineProperty(file, "text", { configurable: true, value: vi.fn(() => body.promise) });
    const view = render(panel());
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });
    await user.type(await screen.findByLabelText("授权原因"), "例行恢复");
    fireEvent.click(screen.getByRole("button", { name: "申请授权并导入" }));
    await waitFor(() => expect(file.text).toHaveBeenCalledTimes(1));
    switchAwayAndBack(view.rerender);
    await act(async () => {
      body.resolve(JSON.stringify({ ssh_keys: [] }));
    });
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(requestConfigImportCredentialGrantMock).not.toHaveBeenCalled();
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
