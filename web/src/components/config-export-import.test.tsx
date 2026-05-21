import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { ConfigExportImport } from "./config-export-import";

const {
  ensureStepUpProofMock,
  requestConfigImportCredentialGrantMock,
  requestConfigExportCredentialGrantMock,
  importConfigMock,
  exportConfigMock,
  toastSuccessMock,
  toastErrorMock,
} = vi.hoisted(() => ({
  ensureStepUpProofMock: vi.fn(),
  requestConfigImportCredentialGrantMock: vi.fn(),
  requestConfigExportCredentialGrantMock: vi.fn(),
  importConfigMock: vi.fn(),
  exportConfigMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: "auth-marker",
    role: "admin",
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
  },
}));

function createImportFile(data: Record<string, unknown>): File {
  const file = new File([JSON.stringify(data)], "xirang-config.json", { type: "application/json" });
  Object.defineProperty(file, "text", {
    configurable: true,
    value: vi.fn().mockResolvedValue(JSON.stringify(data)),
  });
  return file;
}

describe("ConfigExportImport", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
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
    vi.spyOn(window, "confirm").mockReturnValue(true);
  });

  it("opens a grant dialog, requests step-up and grant, then imports without storing grant material", async () => {
    const user = userEvent.setup();
    render(<ConfigExportImport />);

    const file = createImportFile({ ssh_keys: [{ name: "safe-entry" }] });
    fireEvent.change(screen.getByLabelText("导入配置"), { target: { files: [file] } });

    expect(await screen.findByRole("dialog", { name: "需要配置导入临时授权" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("授权原因"), "例行恢复");
    await user.click(screen.getByRole("button", { name: "申请授权并导入" }));

    await waitFor(() => expect(requestConfigImportCredentialGrantMock).toHaveBeenCalledTimes(1));
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
    render(<ConfigExportImport />);

    await user.click(screen.getByRole("button", { name: "导出含敏感字段配置" }));
    expect(await screen.findByRole("dialog", { name: "需要敏感配置导出临时授权" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("授权原因"), "例行导出");
    await user.click(screen.getByRole("button", { name: "申请授权并导出" }));

    await waitFor(() => expect(requestConfigExportCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(apiClient.requestConfigExportCredentialGrant).toHaveBeenCalledWith(
      "auth-marker",
      { reason: "例行导出", requestedTtlSeconds: 600 },
      "step-up-marker",
    );
    expect(apiClient.exportConfig).toHaveBeenCalledWith("auth-marker", true, "step-up-marker");
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
    render(<ConfigExportImport />);

    await user.click(screen.getByRole("button", { name: "导出含敏感字段配置" }));
    await user.click(await screen.findByRole("button", { name: "申请授权并导出" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("请填写授权原因。");
    expect(requestConfigExportCredentialGrantMock).not.toHaveBeenCalled();
    expect(exportConfigMock).not.toHaveBeenCalledWith("auth-marker", true, "step-up-marker");
  });

  it("renders sensitive export grant errors as text without session expiry redirect", async () => {
    const user = userEvent.setup();
    requestConfigExportCredentialGrantMock.mockRejectedValueOnce(new Error("需要临时授权 <script>alert(1)</script>"));
    render(<ConfigExportImport />);

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
    render(<ConfigExportImport />);

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
    render(<ConfigExportImport />);

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
    render(<ConfigExportImport />);

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
    render(<ConfigExportImport />);

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
    ));
    expect(nextFile.text).toHaveBeenCalledTimes(2);
  });
});
