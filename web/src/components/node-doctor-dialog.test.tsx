import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n from "@/i18n";
import { beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier } from "@/lib/api/core";
import type { NodeDoctorResult, NodeRecord } from "@/types/domain";
import { NodeDoctorDialog, formatNodeDoctorCopyText } from "./node-doctor-dialog";
import type { NodeDoctorDialogProps } from "./node-doctor-dialog";

const { toastSuccess, toastError } = vi.hoisted(() => ({
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: toastSuccess, error: toastError },
}));

const SECRET_ADDRESS = "10.20.30.40";
const SECRET_HOST = "db-primary.internal.example";
const SECRET_KEY = "-----BEGIN OPENSSH PRIVATE KEY-----FAKE_KEY_FOR_TEST_ONLY";

const node = {
  id: 7,
  name: "生产数据库节点",
  host: SECRET_HOST,
  address: SECRET_ADDRESS,
  ip: SECRET_ADDRESS,
  port: 2222,
  username: "backup-operator",
  authType: "key",
  keyId: "key-1",
  privateKey: SECRET_KEY,
  basePath: "/srv/secret-backups",
  tags: ["prod"],
  status: "online",
  lastSeenAt: "2026-05-17 10:00:00",
  lastBackupAt: "2026-05-17 09:00:00",
} as unknown as NodeRecord;

const result: NodeDoctorResult = {
  nodeId: 7,
  nodeName: "生产数据库节点",
  generatedAt: "2026-05-17T10:00:00Z",
  checks: [
    { check: "ssh", status: "pass", evidence: "SSH 握手成功", suggestion: "" },
    { check: "disk", status: "warn", evidence: "", suggestion: "清理旧快照释放空间。" },
  ],
};

function t(key: string, options?: Record<string, unknown>) {
  return i18n.t(key, options);
}

function renderDialog(overrides: Partial<NodeDoctorDialogProps> = {}) {
  const props: NodeDoctorDialogProps = {
    open: true,
    node,
    result,
    loading: false,
    error: null,
    onOpenChange: vi.fn(),
    onRun: vi.fn(),
    canOperateNodes: true,
    connectionNavigationAvailable: true,
    onNavigateConnection: vi.fn(() => true),
    ...overrides,
  };
  const view = render(<NodeDoctorDialog {...props} />);
  return {
    ...view,
    props,
    rerenderWith(next: Partial<NodeDoctorDialogProps>) {
      Object.assign(props, next);
      view.rerender(<NodeDoctorDialog {...props} />);
    },
  };
}

function createDeferred() {
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function installDeferredClipboard() {
  const writes: Array<{ text: string; resolve: () => void; reject: (error: unknown) => void }> = [];
  const writeText = vi.fn((text: string) => {
    const deferred = createDeferred();
    writes.push({ text, resolve: deferred.resolve, reject: deferred.reject });
    return deferred.promise;
  });
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  return { writeText, writes };
}

function copyButton() {
  return screen.getByRole("button", { name: t("nodes.doctorCopy") });
}

describe("formatNodeDoctorCopyText", () => {
  it("只输出标题、生成时间、摘要和每项安全诊断字段", () => {
    const text = formatNodeDoctorCopyText(result, i18n.t);

    expect(text).toBe([
      [
        t("nodes.doctorCopyTitle"),
        t("nodes.doctorGeneratedAt", { time: "2026-05-17T10:00:00Z" }),
        t("nodes.doctorSummary", { total: 2, pass: 1, warn: 1, fail: 0, skip: 0 }),
      ].join("\n"),
      [`${t("nodes.doctorStatus_pass")} · ${t("nodes.doctorCheck_ssh")}`, "SSH 握手成功"].join("\n"),
      [
        `${t("nodes.doctorStatus_warn")} · ${t("nodes.doctorCheck_disk")}`,
        t("nodes.doctorNoEvidence"),
        `${t("nodes.doctorSuggestionPrefix")}清理旧快照释放空间。`,
      ].join("\n"),
    ].join("\n\n"));
    expect(text).not.toContain("生产数据库节点");
    expect(text).not.toContain(SECRET_ADDRESS);
    expect(text).not.toContain(SECRET_HOST);
    expect(text).not.toContain("{");
  });

  it("空检查列表仍是可复制的成功结果，不编造检查项", () => {
    const text = formatNodeDoctorCopyText({ ...result, checks: [] }, i18n.t);

    expect(text).toBe([
      t("nodes.doctorCopyTitle"),
      t("nodes.doctorGeneratedAt", { time: "2026-05-17T10:00:00Z" }),
      t("nodes.doctorSummary", { total: 0, pass: 0, warn: 0, fail: 0, skip: 0 }),
    ].join("\n"));
  });
});

describe("NodeDoctorDialog 复制", () => {
  beforeEach(() => {
    toastSuccess.mockReset();
    toastError.mockReset();
    clearAuthTransitionBarrier();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
  });

  it("复制成功写入安全文本且只在完成后提示一次；不含 NodeRecord 敏感字段", async () => {
    const user = userEvent.setup();
    renderDialog();
    const clipboard = installDeferredClipboard();

    await user.click(copyButton());
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
    expect(copyButton()).toBeDisabled();
    expect(toastSuccess).not.toHaveBeenCalled();

    const written = clipboard.writes[0].text;
    expect(written).toBe(formatNodeDoctorCopyText(result, i18n.t));
    for (const secret of [SECRET_ADDRESS, SECRET_HOST, SECRET_KEY, "/srv/secret-backups", "backup-operator", "2222"]) {
      expect(written).not.toContain(secret);
    }

    await act(async () => {
      clipboard.writes[0].resolve();
    });
    expect(toastSuccess).toHaveBeenCalledTimes(1);
    expect(toastSuccess).toHaveBeenCalledWith(t("common.copiedToClipboard"));
    expect(toastError).not.toHaveBeenCalled();
    expect(copyButton()).toBeEnabled();
  });

  it("空检查结果也可复制", async () => {
    const user = userEvent.setup();
    const emptyResult = { ...result, checks: [] };
    renderDialog({ result: emptyResult });
    const clipboard = installDeferredClipboard();

    await user.click(copyButton());
    expect(clipboard.writes[0].text).toBe(formatNodeDoctorCopyText(emptyResult, i18n.t));
  });

  it("Clipboard API 缺失时提示复制失败", async () => {
    const user = userEvent.setup();
    renderDialog();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });

    await user.click(copyButton());
    expect(toastError).toHaveBeenCalledWith(t("common.copyFailed"));
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(copyButton()).toBeEnabled();
  });

  it("剪贴板拒绝时提示复制失败", async () => {
    const user = userEvent.setup();
    renderDialog();
    const clipboard = installDeferredClipboard();

    await user.click(copyButton());
    await act(async () => {
      clipboard.writes[0].reject(new Error("denied"));
    });
    expect(toastError).toHaveBeenCalledTimes(1);
    expect(toastError).toHaveBeenCalledWith(t("common.copyFailed"));
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("同步双击只发起一次写入", () => {
    renderDialog();
    const clipboard = installDeferredClipboard();

    const button = copyButton();
    fireEvent.click(button);
    fireEvent.click(button);
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["关闭", { open: false }],
    ["重新运行清空结果", { result: null, loading: true }],
    ["换节点", { node: { ...node, id: 8 } as NodeRecord, result: { ...result, nodeId: 8 } }],
  ] as const)("%s后旧复制完成不提示", async (_label, change) => {
    const user = userEvent.setup();
    const view = renderDialog();
    const clipboard = installDeferredClipboard();

    await user.click(copyButton());
    view.rerenderWith(change);
    await act(async () => {
      clipboard.writes[0].resolve();
    });
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("关闭重开后旧失败不提示，新结果立即可复制", async () => {
    const user = userEvent.setup();
    const view = renderDialog();
    const clipboard = installDeferredClipboard();

    await user.click(copyButton());
    view.rerenderWith({ open: false, result: null });
    const freshResult = { ...result, generatedAt: "2026-05-17T11:00:00Z" };
    view.rerenderWith({ open: true, result: freshResult });
    await waitFor(() => expect(copyButton()).toBeEnabled());

    await act(async () => {
      clipboard.writes[0].reject(new Error("late denial"));
    });
    expect(toastError).not.toHaveBeenCalled();

    await user.click(copyButton());
    expect(clipboard.writeText).toHaveBeenCalledTimes(2);
    expect(clipboard.writes[1].text).toContain("2026-05-17T11:00:00Z");
  });

  it("会话代次变化后迟到完成不提示", async () => {
    const user = userEvent.setup();
    renderDialog();
    const clipboard = installDeferredClipboard();

    await user.click(copyButton());
    bumpAuthSessionGeneration();
    await act(async () => {
      clipboard.writes[0].resolve();
    });
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("auth barrier 期间不发起复制，激活后迟到完成不提示", async () => {
    const user = userEvent.setup();
    renderDialog();
    const clipboard = installDeferredClipboard();

    await user.click(copyButton());
    beginAuthTransitionBarrier();
    await act(async () => {
      clipboard.writes[0].resolve();
    });
    expect(toastSuccess).not.toHaveBeenCalled();

    fireEvent.click(copyButton());
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["加载中", { loading: true }],
    ["有错误", { error: "诊断失败" }],
    ["无结果", { result: null }],
    ["结果节点不匹配", { result: { ...result, nodeId: 99 } }],
    ["无操作角色", { canOperateNodes: false }],
  ] as const)("%s时复制不可用", (_label, change) => {
    renderDialog(change);
    expect(copyButton()).toBeDisabled();
  });
});

describe("NodeDoctorDialog 前往连接测试", () => {
  const unavailableText = () => t("nodes.doctorConnectionUnavailable");
  const navButtonName = () => t("nodes.doctorGoToConnectionTest");

  it("仅操作角色显示入口", () => {
    renderDialog({ canOperateNodes: false });
    expect(screen.queryByRole("button", { name: navButtonName() })).not.toBeInTheDocument();
  });

  it("回调返回 true 时不显示不可用说明", async () => {
    const user = userEvent.setup();
    const onNavigateConnection = vi.fn(() => true);
    renderDialog({ onNavigateConnection });

    await user.click(screen.getByRole("button", { name: navButtonName() }));
    expect(onNavigateConnection).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(unavailableText())).not.toBeInTheDocument();
  });

  it("回调返回 false 时显示说明，换节点后清除", async () => {
    const user = userEvent.setup();
    const view = renderDialog({ onNavigateConnection: vi.fn(() => false) });

    await user.click(screen.getByRole("button", { name: navButtonName() }));
    expect(screen.getByText(unavailableText())).toBeInTheDocument();

    view.rerenderWith({ node: { ...node, id: 8 } as NodeRecord, result: { ...result, nodeId: 8 } });
    expect(screen.queryByText(unavailableText())).not.toBeInTheDocument();
  });

  it("基础不可用时禁用入口并说明原因", () => {
    const onNavigateConnection = vi.fn(() => true);
    renderDialog({ connectionNavigationAvailable: false, onNavigateConnection });

    const button = screen.getByRole("button", { name: navButtonName() });
    expect(button).toBeDisabled();
    expect(screen.getByText(unavailableText())).toBeInTheDocument();
    fireEvent.click(button);
    expect(onNavigateConnection).not.toHaveBeenCalled();
  });
});
