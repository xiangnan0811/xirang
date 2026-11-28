import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { runAxe } from "@/test/a11y-helpers";
import { ApiError } from "@/lib/api/core";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";
import { NodeEditorDialog } from "./node-editor-dialog";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const sshKeys: SSHKeyRecord[] = [
  {
    id: "key-1",
    name: "prod-ed25519",
    username: "root",
    keyType: "ed25519",
    fingerprint: "SHA256:active",
    broadScope: false,
    createdAt: "2026-01-01 00:00:00",
    disabled: false,
    allowedPurposes: "",
    allowedNodeIds: "",
    allowedNodeTags: "",
  },
];

function makeNode(overrides: Partial<NodeRecord> = {}): NodeRecord {
  return {
    id: 10,
    name: "prod-node-1",
    host: "10.10.0.11",
    address: "10.10.0.11",
    ip: "10.10.0.11",
    port: 2222,
    username: "deploy",
    authType: "key",
    keyId: "key-1",
    basePath: "/srv/app",
    status: "online",
    tags: ["prod", "web"],
    rawTags: "prod,web",
    lastSeenAt: "2026-04-01 10:00:00",
    lastBackupAt: "2026-04-01 09:00:00",
    backupDir: "prod-node-1",
    useSudo: true,
    ...overrides,
  };
}

type SaveFn = Parameters<typeof NodeEditorDialog>[0]["onSave"];

function renderEditor(props: Partial<Parameters<typeof NodeEditorDialog>[0]> = {}) {
  const onSave = vi.fn<SaveFn>().mockResolvedValue(undefined);
  const onTestConnection = vi.fn<(nodeId: number) => Promise<void>>().mockResolvedValue(undefined);
  const view = render(
    <NodeEditorDialog
      open
      onOpenChange={() => {}}
      sshKeys={sshKeys}
      onSave={onSave}
      onTestConnection={onTestConnection}
      {...props}
    />,
  );
  return { ...view, onSave: (props.onSave as typeof onSave | undefined) ?? onSave, onTestConnection };
}

const submitButton = () => screen.getByRole("button", { name: "保存并测试" });
const testButton = () => screen.getByRole("button", { name: /测试连接|nodeEditor\.testingConnection|测试中/ });

const REVISION_OPENED = "0123456789abcdef0123456789abcdef";
const REVISION_SAVED = "fedcba9876543210fedcba9876543210";
const revisionConflict = () =>
  new ApiError(409, "conflict", { code: 409, message: "conflict", data: { error_code: "node_revision_conflict" } });
const conflictNotice = /该节点已在别处被修改/;
const reloadButton = () => screen.getByRole("button", { name: /重新加载已保存的节点|正在重新加载已保存的节点/ });

describe("NodeEditorDialog", () => {
  const originalTZ = process.env.TZ;
  afterEach(() => {
    if (originalTZ === undefined) {
      delete process.env.TZ;
    } else {
      process.env.TZ = originalTZ;
    }
  });

  it("creates with the supplied defaults and labeled fields", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();

    expect(screen.getByLabelText("基础路径")).toHaveValue("/");
    expect(screen.getByLabelText("端口")).toHaveValue("22");

    await user.type(screen.getByLabelText("节点名称"), "edge-01");
    await user.type(screen.getByLabelText("主机地址"), "10.0.0.8");
    await user.selectOptions(screen.getByLabelText("SSH 密钥"), "key-1");
    const backupDir = screen.getByLabelText("备份目录标识");
    expect(backupDir).toHaveValue("edge-01");
    expect(backupDir).toHaveAccessibleDescription("留空时根据节点名称自动生成。备份数据的实际存放位置以各任务配置为准。");
    expect(screen.queryByText(/\/backup\//)).not.toBeInTheDocument();
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    const [input, nodeId] = onSave.mock.calls[0];
    expect(nodeId).toBeUndefined();
    expect(input).toMatchObject({
      name: "edge-01",
      host: "10.0.0.8",
      port: 22,
      username: "root",
      authType: "key",
      keyId: "key-1",
      basePath: "/",
      backupDir: "edge-01",
      maintenanceStart: "",
      maintenanceEnd: "",
    });
  });

  it("preserves raw tags, blank base path and untouched maintenance on an unrelated edit", async () => {
    process.env.TZ = "America/New_York";
    const user = userEvent.setup();
    const { onSave } = renderEditor({
      editingNode: makeNode({
        tags: ["prod", "web"],
        rawTags: " prod,, web ",
        basePath: "",
        // 01:30 EST — the second, repeated 01:30 of the 2026 fall-back night.
        maintenanceStart: "2026-11-01T06:30:45Z",
        maintenanceEnd: "2026-11-01T09:00:00Z",
      }),
    });

    expect(screen.getByLabelText("标签")).toHaveValue(" prod,, web ");
    expect(screen.getByLabelText("基础路径")).toHaveValue("");

    await user.type(screen.getByLabelText("节点名称"), "-renamed");
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    const [input, nodeId] = onSave.mock.calls[0];
    expect(nodeId).toBe(10);
    expect(input.name).toBe("prod-node-1-renamed");
    expect(input.tags).toBe(" prod,, web ");
    expect(input.basePath).toBeUndefined();
    expect(input.port).toBe(2222);
    expect(input.backupDir).toBe("prod-node-1");
    expect(input.maintenanceStart).toBeUndefined();
    expect(input.maintenanceEnd).toBeUndefined();
    expect(input.expiryDate).toBeUndefined();
  });

  const basePathHint = "填写以 / 开头的绝对路径，如 /srv/data。该节点的文件浏览仅限此目录及任务源路径。";

  it.each([
    { scenario: "a leading space", value: " /srv/app", error: "请去掉首尾的空白字符。" },
    // NEL 属于 Go 的 Unicode 空白（String#trim 不认）。
    { scenario: "a trailing NEL", value: "/srv/app\u0085", error: "请去掉首尾的空白字符。" },
    { scenario: "a relative path", value: "srv/app", error: "请填写以 / 开头的绝对路径。" },
    { scenario: "a .. segment", value: "/srv/../etc", error: "路径不能包含 .. 路径段。" },
    { scenario: "a trailing .. segment", value: "/srv/..", error: "路径不能包含 .. 路径段。" },
    { scenario: "a NUL character", value: "/srv\0app", error: "路径不能包含空字符或无效字符。" },
  ])("rejects a changed base path with $scenario inline", async ({ value, error }) => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode() });
    const field = screen.getByLabelText("基础路径");

    fireEvent.change(field, { target: { value } });
    await user.click(submitButton());

    expect(onSave).not.toHaveBeenCalled();
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveFocus();
    expect(field).toHaveAccessibleDescription(`${error} ${basePathHint}`);

    fireEvent.change(field, { target: { value: "/srv/app" } });
    expect(field).toHaveAttribute("aria-invalid", "false");
  });

  it("saves a valid base path with dot names, internal spaces and redundant slashes exactly as typed", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode() });
    const value = "/srv/.cache/..hidden/a..b/My Files//./";

    fireEvent.change(screen.getByLabelText("基础路径"), { target: { value } });
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].basePath).toBe(value);
  });

  it("prefills / but leaves the path unset when it is cleared on create", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    const field = screen.getByLabelText("基础路径");
    expect(field).toHaveValue("/");

    await user.type(screen.getByLabelText("节点名称"), "edge-01");
    await user.type(screen.getByLabelText("主机地址"), "10.0.0.8");
    await user.selectOptions(screen.getByLabelText("SSH 密钥"), "key-1");
    fireEvent.change(field, { target: { value: "  " } });
    await user.click(submitButton());
    expect(onSave).not.toHaveBeenCalled();
    expect(field).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(field, { target: { value: "" } });
    expect(field).toHaveAccessibleDescription(`${basePathHint} 当前仅可浏览任务源路径。填写基础路径可添加授权目录。`);
    await user.click(submitButton());
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].basePath).toBe("");
  });

  it("omits a cleared base path on edit to keep the stored one", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode() });
    const field = screen.getByLabelText("基础路径");

    expect(field).toHaveAccessibleDescription(basePathHint);
    await user.clear(field);
    expect(field).toHaveAccessibleDescription(`${basePathHint} 留空将保留当前基础路径（/srv/app）。`);
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].basePath).toBeUndefined();
  });

  it("omits a legacy path during unrelated edits so it cannot overwrite a concurrent repair", async () => {
    const user = userEvent.setup();
    const legacy = "srv/../legacy ";
    const { onSave } = renderEditor({ editingNode: makeNode({ basePath: legacy }) });
    const field = screen.getByLabelText("基础路径");

    expect(field).toHaveValue(legacy);
    await user.type(screen.getByLabelText("节点名称"), "-renamed");
    await user.click(submitButton());

    expect(field).toHaveAttribute("aria-invalid", "false");
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].basePath).toBeUndefined();
  });

  it("explains that a stored whitespace-only base path allows only task source paths", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode({ basePath: " \t" }) });
    const field = screen.getByLabelText("基础路径");
    const note = "当前保存的基础路径仅含空白字符，因此仅可浏览任务源路径。填写绝对路径可添加授权目录。";

    expect(field).toHaveAccessibleDescription(`${basePathHint} ${note}`);
    await user.clear(field);
    expect(field).toHaveAccessibleDescription(`${basePathHint} ${note}`);
    fireEvent.change(field, { target: { value: " \t" } });
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].basePath).toBeUndefined();
  });

  it("treats a Go-blank backup ID as keeping the stored one, without a migration notice", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode() });
    const field = screen.getByLabelText("备份目录标识");
    const editHint = "留空则保留当前标识。备份数据的实际存放位置以各任务配置为准。";

    expect(field).toHaveAccessibleDescription(editHint);
    expect(screen.queryByText(/\/backup\//)).not.toBeInTheDocument();

    // NEL 属于 Go 的 Unicode 空白（String#trim 不认），服务端会保留原标识。
    fireEvent.change(field, { target: { value: " \u0085\t" } });
    expect(field).toHaveAccessibleDescription(editHint);
    expect(screen.queryByText("修改此标识不会自动迁移已有备份数据。")).not.toBeInTheDocument();

    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].backupDir).toBe(" \u0085\t");
  });

  it("warns before saving a backup ID that differs exactly from the stored one and sends it raw", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode() });
    const field = screen.getByLabelText("备份目录标识");
    const notice = "修改此标识不会自动迁移已有备份数据。";

    fireEvent.change(field, { target: { value: "prod-node-2" } });
    expect(field).toHaveAccessibleDescription(/修改此标识不会自动迁移已有备份数据。$/);

    fireEvent.change(field, { target: { value: "prod-node-1" } });
    expect(screen.queryByText(notice)).not.toBeInTheDocument();

    // BOM 不是 Go 空白（String#trim 会去掉它），保存后成为新的标识。
    fireEvent.change(field, { target: { value: "\uFEFF" } });
    expect(screen.getByText(notice)).toBeInTheDocument();

    // 服务端按原始字节计长度，首尾空白不会被裁掉。
    fireEvent.change(field, { target: { value: `${"a".repeat(128)} ` } });
    await user.click(submitButton());
    expect(onSave).not.toHaveBeenCalled();
    expect(field).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(field, { target: { value: "prod-node-1 " } });
    expect(field).toHaveAccessibleDescription(/修改此标识不会自动迁移已有备份数据。$/);
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].backupDir).toBe("prod-node-1 ");
  });

  it("requires an explicit backup ID when the edited node has none stored", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode({ backupDir: "" }) });
    const field = screen.getByLabelText("备份目录标识");
    const missingHint = "此节点尚未设置备份目录标识，请填写。备份数据的实际存放位置以各任务配置为准。";

    expect(field).toHaveAccessibleDescription(missingHint);
    await user.type(screen.getByLabelText("节点名称"), "-已重命名");
    expect(field).toHaveValue("");
    expect(screen.queryByText(/自动生成的备份目录标识/)).not.toBeInTheDocument();

    await user.click(submitButton());

    expect(onSave).not.toHaveBeenCalled();
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveFocus();
    expect(field).toHaveAccessibleDescription(`请填写备份目录标识，此节点当前没有可保留的标识。 ${missingHint}`);

    await user.type(field, "edge-legacy");
    expect(field).toHaveAttribute("aria-invalid", "false");
    expect(field).toHaveAccessibleDescription(`${missingHint} 修改此标识不会自动迁移已有备份数据。`);
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].backupDir).toBe("edge-legacy");
  });

  it("keeps a stored whitespace-only legacy backup ID on an unrelated edit", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode({ backupDir: "  " }) });
    const field = screen.getByLabelText("备份目录标识");

    expect(field).toHaveAccessibleDescription("留空则保留当前标识。备份数据的实际存放位置以各任务配置为准。");

    await user.type(screen.getByLabelText("节点名称"), "-renamed");
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].backupDir).toBe("  ");
  });

  it("falls back to joined tags when the raw string no longer matches the parsed tags", () => {
    renderEditor({ editingNode: makeNode({ tags: ["db"], rawTags: "prod,web" }) });
    expect(screen.getByLabelText("标签")).toHaveValue("db");
  });

  it("sends empty strings when the maintenance window is cleared on purpose", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({
      editingNode: makeNode({
        maintenanceStart: "2026-07-01T02:00:00Z",
        maintenanceEnd: "2026-07-01T04:00:00Z",
      }),
    });

    fireEvent.change(screen.getByLabelText("维护窗口开始"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("维护窗口结束"), { target: { value: "" } });
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0]).toMatchObject({ maintenanceStart: "", maintenanceEnd: "" });
  });

  it("does not demand repairs to legacy fields the user did not touch", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({
      editingNode: makeNode({
        name: "legacy/node",
        keyId: null,
        maintenanceStart: "2026-07-01T02:00:00Z",
        maintenanceEnd: undefined,
      }),
    });

    await user.clear(screen.getByLabelText("标签"));
    await user.type(screen.getByLabelText("标签"), "db");
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0]).toMatchObject({
      name: "legacy/node",
      keyId: null,
      tags: "db",
      maintenanceStart: undefined,
      maintenanceEnd: undefined,
    });
  });

  it("blocks invalid edits inline and focuses the first invalid field", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();

    await user.clear(screen.getByLabelText("端口"));
    await user.type(screen.getByLabelText("端口"), "70000");
    await user.click(submitButton());

    expect(onSave).not.toHaveBeenCalled();
    const name = screen.getByLabelText("节点名称");
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(name).toHaveFocus();
    expect(screen.getByLabelText("主机地址")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("端口")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("SSH 密钥")).toHaveAttribute("aria-invalid", "true");

    await user.type(name, "x");
    expect(name).toHaveAttribute("aria-invalid", "false");
  });

  it("requires both ends of an edited maintenance window", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode() });

    fireEvent.change(screen.getByLabelText("维护窗口开始"), { target: { value: "2026-07-01T10:00" } });
    await user.click(submitButton());

    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByLabelText("维护窗口结束")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("维护窗口结束")).toHaveFocus();
  });

  it("accepts an edited start just before a stored end that is only microseconds later", async () => {
    process.env.TZ = "UTC";
    const user = userEvent.setup();
    const { onSave } = renderEditor({
      editingNode: makeNode({
        maintenanceStart: "2026-07-01T08:00:00Z",
        // = 10:00:00.000001Z：与编辑后的开始时间毫秒相同，仅晚 1 微秒，服务端视为晚于开始。
        maintenanceEnd: "2026-07-01T12:00:00.000001+02:00",
      }),
    });
    const end = screen.getByLabelText("维护窗口结束");
    expect(end).toHaveValue("2026-07-01T10:00");

    fireEvent.change(screen.getByLabelText("维护窗口开始"), { target: { value: "2026-07-01T10:00" } });
    await user.click(submitButton());

    expect(end).toHaveAttribute("aria-invalid", "false");
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].maintenanceStart).toBe("2026-07-01T10:00:00.000Z");
    expect(onSave.mock.calls[0][0].maintenanceEnd).toBeUndefined();
  });

  it.each([
    {
      scenario: "a stored start 1µs after the edited end",
      node: { maintenanceStart: "2026-07-01T10:00:00.000001Z", maintenanceEnd: "2026-07-01T12:00:00Z" },
      edited: "维护窗口结束",
    },
    {
      scenario: "a stored end equal to the edited start after microsecond truncation",
      node: { maintenanceStart: "2026-07-01T08:00:00Z", maintenanceEnd: "2026-07-01T10:00:00.000000999Z" },
      edited: "维护窗口开始",
    },
  ])("rejects an edited endpoint against $scenario", async ({ node, edited }) => {
    process.env.TZ = "UTC";
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode(node) });

    fireEvent.change(screen.getByLabelText(edited), { target: { value: "2026-07-01T10:00" } });
    await user.click(submitButton());

    expect(onSave).not.toHaveBeenCalled();
    const end = screen.getByLabelText("维护窗口结束");
    expect(end).toHaveAttribute("aria-invalid", "true");
    expect(end).toHaveAccessibleDescription(/结束时间必须晚于开始时间。/);
  });

  it("checks an edited end against the untouched legacy start's exact stored instant", async () => {
    process.env.TZ = "America/New_York";
    const user = userEvent.setup();
    const { onSave } = renderEditor({
      editingNode: makeNode({
        // 夏令时回拨夜第二个 01:30（EST）：非 UTC 偏移且带微秒，本地分钟值无法区分重复时刻。
        maintenanceStart: "2026-11-01T01:30:00.000001-05:00",
        maintenanceEnd: "2026-11-01T03:00:00-05:00",
      }),
    });
    const start = screen.getByLabelText("维护窗口开始");
    const end = screen.getByLabelText("维护窗口结束");
    expect(start).toHaveValue("2026-11-01T01:30");

    // 01:45 按较早偏移（EDT）解析为 05:45Z，早于存储的 06:30:00.000001Z。
    fireEvent.change(end, { target: { value: "2026-11-01T01:45" } });
    await user.click(submitButton());
    expect(onSave).not.toHaveBeenCalled();
    expect(end).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(end, { target: { value: "2026-11-01T02:00" } });
    await user.click(submitButton());

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0].maintenanceStart).toBeUndefined();
    expect(onSave.mock.calls[0][0].maintenanceEnd).toBe("2026-11-01T07:00:00.000Z");
  });

  it("requires a password only when switching to password auth", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor({ editingNode: makeNode() });

    await user.selectOptions(screen.getByLabelText("认证方式"), "password");
    await user.click(submitButton());
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByLabelText("SSH 密码")).toHaveAttribute("aria-invalid", "true");

    await user.type(screen.getByLabelText("SSH 密码"), "secret");
    await user.click(submitButton());
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0]).toMatchObject({ authType: "password", password: "secret", keyId: null });
  });

  it("locks fields and actions while saving and ignores repeat submits", async () => {
    const user = userEvent.setup();
    const pending = deferred<void>();
    const onSave = vi.fn<SaveFn>().mockReturnValue(pending.promise);
    renderEditor({ editingNode: makeNode(), onSave });

    await user.click(submitButton());
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText("节点名称")).toBeDisabled();
    expect(testButton()).toBeDisabled();

    fireEvent.submit(screen.getByLabelText("节点名称").closest("form")!);
    expect(onSave).toHaveBeenCalledTimes(1);

    pending.resolve();
    await waitFor(() => expect(screen.getByLabelText("节点名称")).not.toBeDisabled());
  });

  it("blocks saving while a connection test runs", async () => {
    const user = userEvent.setup();
    const pending = deferred<void>();
    const { onSave, onTestConnection } = renderEditor({ editingNode: makeNode() });
    onTestConnection.mockReturnValue(pending.promise);

    await user.click(testButton());
    expect(onTestConnection).toHaveBeenCalledWith(10);
    expect(submitButton()).toBeDisabled();
    fireEvent.submit(screen.getByLabelText("节点名称").closest("form")!);
    expect(onSave).not.toHaveBeenCalled();

    pending.resolve();
    await waitFor(() => expect(submitButton()).not.toBeDisabled());
  });

  it("keeps testing the saved configuration available with unsaved connection edits", async () => {
    const user = userEvent.setup();
    const pending = deferred<void>();
    const { onSave, onTestConnection } = renderEditor({ editingNode: makeNode() });
    onTestConnection.mockReturnValue(pending.promise);

    expect(testButton()).not.toHaveAttribute("aria-describedby");
    await user.type(screen.getByLabelText("主机地址"), "9");

    expect(testButton()).toBeEnabled();
    expect(testButton()).toHaveAttribute("aria-describedby", "node-edit-test-dirty-hint");
    expect(document.getElementById("node-edit-test-dirty-hint")).toBeInTheDocument();

    await user.click(testButton());
    expect(onTestConnection).toHaveBeenCalledWith(10);
    expect(testButton()).toBeDisabled();
    await user.click(testButton());
    fireEvent.submit(screen.getByLabelText("节点名称").closest("form")!);
    expect(onTestConnection).toHaveBeenCalledTimes(1);
    expect(onSave).not.toHaveBeenCalled();

    pending.resolve();
    await waitFor(() => expect(testButton()).toBeEnabled());
    expect(screen.getByLabelText("主机地址")).toHaveValue("10.10.0.119");
  });

  it("cannot be dismissed while a save is in flight and keeps focus inside", async () => {
    const user = userEvent.setup();
    const pending = deferred<void>();
    const onOpenChange = vi.fn();
    const onSave = vi.fn<SaveFn>().mockReturnValue(pending.promise);
    renderEditor({ editingNode: makeNode(), onSave, onOpenChange });

    await user.click(submitButton());
    const dialog = screen.getByRole("dialog");
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
    expect(screen.getByRole("button", { name: /Cancel|取消/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();

    await user.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await act(async () => {
      pending.reject(new Error("名称已被占用"));
      await pending.promise.catch(() => {});
    });
    expect(await screen.findByText("名称已被占用")).toBeInTheDocument();
    expect(submitButton()).toHaveFocus();
    expect(screen.getByRole("button", { name: /Cancel|取消/ })).toBeEnabled();

    await user.keyboard("{Escape}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("ignores a save from an earlier opening after the editor is closed and reopened", async () => {
    const user = userEvent.setup();
    const stale = deferred<void>();
    const current = deferred<void>();
    let staleKeyCreated: ((keyId: string) => void) | undefined;
    const onSave = vi
      .fn<SaveFn>()
      .mockImplementationOnce((_input, _nodeId, onKeyCreated) => {
        staleKeyCreated = onKeyCreated;
        return stale.promise;
      })
      .mockImplementationOnce(() => current.promise);
    const node = makeNode();
    const shared = { onOpenChange: () => {}, sshKeys, onSave };
    const { rerender } = render(<NodeEditorDialog open editingNode={node} {...shared} />);

    await user.click(submitButton());
    expect(screen.getByLabelText("节点名称")).toBeDisabled();

    // 外部关闭（如鉴权重置）后再次打开同一节点，是一次新的会话。
    rerender(<NodeEditorDialog open={false} editingNode={null} {...shared} />);
    rerender(<NodeEditorDialog open editingNode={node} {...shared} />);
    await waitFor(() => expect(screen.getByLabelText("节点名称")).toHaveValue("prod-node-1"));
    expect(screen.getByLabelText("节点名称")).not.toBeDisabled();

    await user.click(submitButton());
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(screen.getByLabelText("节点名称")).toBeDisabled();

    await act(async () => {
      staleKeyCreated?.("key-stale");
      stale.reject(new Error("旧会话保存失败"));
      await stale.promise.catch(() => {});
    });
    expect(screen.queryByText("旧会话保存失败")).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /key-stale/ })).not.toBeInTheDocument();
    // 旧会话的 finally 不得释放新会话的保存锁。
    expect(screen.getByLabelText("节点名称")).toBeDisabled();
    expect(screen.getByLabelText("SSH 密钥")).toHaveValue("key-1");

    await act(async () => {
      current.resolve();
      await current.promise;
    });
    expect(screen.getByLabelText("节点名称")).not.toBeDisabled();
  });

  it("binds the key created by a failed save so a retry reuses it", async () => {
    const user = userEvent.setup();
    const onSave = vi
      .fn<SaveFn>()
      .mockImplementationOnce(async (_input, _nodeId, onKeyCreated) => {
        onKeyCreated("key-new");
        throw new Error("节点名称已存在");
      })
      .mockResolvedValueOnce(undefined);
    renderEditor({ onSave });

    await user.type(screen.getByLabelText("节点名称"), "edge-02");
    await user.type(screen.getByLabelText("主机地址"), "10.0.0.9");
    await user.type(screen.getByLabelText("标签"), "edge");
    await user.selectOptions(screen.getByLabelText("SSH 密钥"), "__new__");
    await user.type(screen.getByLabelText("密钥名称"), "edge-key");
    await user.type(screen.getByLabelText("内联新密钥"), "PRIVATE KEY");
    await user.click(submitButton());

    expect(onSave.mock.calls[0][0]).toMatchObject({ keyId: null, inlineKeyName: "edge-key", inlinePrivateKey: "PRIVATE KEY" });
    expect(await screen.findByText("节点名称已存在")).toBeInTheDocument();
    // 新密钥尚未出现在列表中时，用已创建的名称兜底显示，并清掉内联字段。
    expect(screen.getByLabelText("SSH 密钥")).toHaveValue("key-new");
    expect(screen.getByRole("option", { name: "edge-key" })).toBeInTheDocument();
    expect(screen.queryByLabelText("内联新密钥")).not.toBeInTheDocument();
    expect(screen.getByLabelText("节点名称")).toHaveValue("edge-02");
    expect(screen.getByLabelText("标签")).toHaveValue("edge");

    await user.click(submitButton());
    expect(onSave).toHaveBeenCalledTimes(2);
    const [retryInput] = onSave.mock.calls[1];
    expect(retryInput).toMatchObject({ name: "edge-02", host: "10.0.0.9", tags: "edge", keyId: "key-new" });
    expect(retryInput.inlinePrivateKey).toBeUndefined();
    expect(retryInput.inlineKeyName).toBeUndefined();
  });

  it("shows a save failure inline and keeps the draft", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn<SaveFn>().mockRejectedValue(new Error("主机地址格式不合法"));
    renderEditor({ editingNode: makeNode(), onSave });

    await user.click(submitButton());

    expect(await screen.findByText("主机地址格式不合法")).toBeInTheDocument();
    expect(screen.getByLabelText("节点名称")).toHaveValue("prod-node-1");
    expect(screen.getByLabelText("节点名称")).not.toBeDisabled();
  });

  it("sends the opened revision and keeps the draft, created key and save lock on a revision conflict", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn<SaveFn>().mockImplementationOnce(async (_input, _nodeId, onKeyCreated) => {
      onKeyCreated("key-new");
      throw revisionConflict();
    });
    const onReloadNode = vi.fn<(nodeId: number) => Promise<NodeRecord | null>>();
    const { onTestConnection } = renderEditor({ editingNode: makeNode({ revision: REVISION_OPENED }), onSave, onReloadNode });

    await user.clear(screen.getByLabelText("主机地址"));
    await user.type(screen.getByLabelText("主机地址"), "10.0.0.99");
    await user.selectOptions(screen.getByLabelText("SSH 密钥"), "__new__");
    await user.type(screen.getByLabelText("密钥名称"), "edge-key");
    await user.type(screen.getByLabelText("内联新密钥"), "PRIVATE KEY");
    await user.click(submitButton());

    expect(onSave.mock.calls[0][0]).toMatchObject({ expectedRevision: REVISION_OPENED, host: "10.0.0.99" });
    expect(await screen.findByText(conflictNotice)).toBeInTheDocument();
    expect(reloadButton()).toBeEnabled();
    expect(reloadButton()).toHaveFocus();
    expect(screen.getByLabelText("主机地址")).toHaveValue("10.0.0.99");
    expect(screen.getByLabelText("SSH 密钥")).toHaveValue("key-new");
    expect(screen.getByRole("option", { name: "edge-key" })).toBeInTheDocument();
    expect(screen.queryByText("conflict")).not.toBeInTheDocument();

    // Neither the button nor an Enter submit can resend the stale form.
    expect(submitButton()).toBeDisabled();
    // Nor can this editor test saved settings it has not seen; the conflict must be resolved by reload first.
    expect(testButton()).toBeDisabled();
    await user.click(testButton());
    expect(onTestConnection).not.toHaveBeenCalled();
    await user.type(screen.getByLabelText("主机地址"), "{Enter}");
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onReloadNode).not.toHaveBeenCalled();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("replaces the draft with the explicitly reloaded node and saves against its revision", async () => {
    const user = userEvent.setup();
    const reload = deferred<NodeRecord | null>();
    const onSave = vi.fn<SaveFn>().mockRejectedValueOnce(revisionConflict()).mockResolvedValueOnce(undefined);
    const onReloadNode = vi.fn<(nodeId: number) => Promise<NodeRecord | null>>(() => reload.promise);
    renderEditor({ editingNode: makeNode({ revision: REVISION_OPENED }), onSave, onReloadNode });

    await user.clear(screen.getByLabelText("标签"));
    await user.type(screen.getByLabelText("标签"), "draft-only");
    await user.click(submitButton());
    await screen.findByText(conflictNotice);
    await user.click(reloadButton());

    expect(onReloadNode).toHaveBeenCalledWith(10);
    expect(reloadButton()).toHaveAccessibleName("正在重新加载已保存的节点…");
    expect(reloadButton()).toBeDisabled();
    // The disabled button cannot keep focus; the conflict notice holds it inside the dialog meanwhile.
    expect(reloadButton()).not.toHaveFocus();
    expect(screen.getByText(conflictNotice).closest("[tabindex='-1']")).toHaveFocus();
    expect(screen.getByLabelText("标签")).toBeDisabled();
    expect(screen.getByLabelText("标签")).toHaveValue("draft-only");

    await act(async () => {
      reload.resolve(makeNode({ host: "10.10.0.77", revision: REVISION_SAVED }));
      await reload.promise;
    });

    await waitFor(() => expect(screen.getByLabelText("主机地址")).toHaveValue("10.10.0.77"));
    expect(screen.getByLabelText("标签")).toHaveValue("prod,web");
    expect(screen.queryByText(conflictNotice)).not.toBeInTheDocument();
    expect(screen.getByLabelText("节点名称")).toHaveFocus();
    expect(submitButton()).toBeEnabled();
    expect(testButton()).toBeEnabled();

    // Later renders of the same session leave focus where the user put it.
    await user.click(screen.getByLabelText("标签"));
    await user.type(screen.getByLabelText("标签"), ",edge");
    expect(screen.getByLabelText("标签")).toHaveFocus();

    await user.click(submitButton());
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(onSave.mock.calls[1][0]).toMatchObject({ expectedRevision: REVISION_SAVED, host: "10.10.0.77" });
  });

  it("keeps the draft and offers the reload again when reloading the saved node fails", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn<SaveFn>().mockRejectedValue(revisionConflict());
    const onReloadNode = vi
      .fn<(nodeId: number) => Promise<NodeRecord | null>>()
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValueOnce(makeNode({ revision: REVISION_SAVED }));
    renderEditor({ editingNode: makeNode({ revision: REVISION_OPENED }), onSave, onReloadNode });

    await user.clear(screen.getByLabelText("标签"));
    await user.type(screen.getByLabelText("标签"), "draft-only");
    await user.click(submitButton());
    await screen.findByText(conflictNotice);
    await user.click(reloadButton());

    expect(await screen.findByText("无法重新加载已保存的节点。草稿已保留，请重试。")).toBeInTheDocument();
    expect(reloadButton()).toBeEnabled();
    expect(reloadButton()).toHaveFocus();
    expect(screen.getByLabelText("标签")).toHaveValue("draft-only");
    expect(screen.getByLabelText("标签")).not.toBeDisabled();
    expect(submitButton()).toBeDisabled();
    expect(testButton()).toBeDisabled();

    // Keyboard retry from the refocused button.
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.getByLabelText("标签")).toHaveValue("prod,web"));
    expect(screen.getByLabelText("节点名称")).toHaveFocus();
    expect(onReloadNode).toHaveBeenCalledTimes(2);
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it("drops a reload that settles after the editor was closed and reopened", async () => {
    const user = userEvent.setup();
    const reload = deferred<NodeRecord | null>();
    const onSave = vi.fn<SaveFn>().mockRejectedValueOnce(revisionConflict());
    const onReloadNode = vi.fn<(nodeId: number) => Promise<NodeRecord | null>>(() => reload.promise);
    const node = makeNode({ revision: REVISION_OPENED });
    const shared = { onOpenChange: () => {}, sshKeys, onSave, onReloadNode };
    const { rerender } = render(<NodeEditorDialog open editingNode={node} {...shared} />);

    await user.click(submitButton());
    await screen.findByText(conflictNotice);
    await user.click(reloadButton());

    rerender(<NodeEditorDialog open={false} editingNode={null} {...shared} />);
    rerender(<NodeEditorDialog open editingNode={node} {...shared} />);
    await waitFor(() => expect(screen.getByLabelText("节点名称")).toHaveValue("prod-node-1"));
    expect(screen.queryByText(conflictNotice)).not.toBeInTheDocument();

    await act(async () => {
      reload.resolve(makeNode({ host: "10.10.0.77", revision: REVISION_SAVED }));
      await reload.promise;
    });
    expect(screen.getByLabelText("主机地址")).toHaveValue("10.10.0.11");
    expect(submitButton()).toBeEnabled();
  });

  it("has no axe violations in create and edit modes", async () => {
    const { unmount } = renderEditor();
    expect(await runAxe(document.body)).toHaveNoViolations();
    unmount();

    renderEditor({ editingNode: makeNode({ basePath: "" }) });
    expect(await runAxe(document.body)).toHaveNoViolations();
  });
});
