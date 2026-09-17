import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { TaskEditorDialog } from "@/components/task-create-dialog";
import { toast } from "@/components/ui/toast-sonner";
import type { NodeRecord, PolicyRecord, TaskRecord } from "@/types/domain";
import { ApiError } from "@/lib/api/core";

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

function createNode(id: number, name: string): NodeRecord {
  return {
    id,
    name,
    host: `${name}.example.com`,
    address: `10.0.0.${id}`,
    ip: `10.0.0.${id}`,
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    status: "online",
    tags: ["prod"],
    lastSeenAt: "2026-03-10 10:00:00",
    lastBackupAt: "2026-03-10 09:30:00",
    diskFreePercent: 80,
    diskUsedGb: 20,
    diskTotalGb: 100,
    diskProbeAt: "2026-03-10 10:00:00",
    connectionLatencyMs: 12,
    backupDir: name,
  };
}

function createPolicy(id: number, name: string): PolicyRecord {
  return {
    id,
    name,
    sourcePath: `/data/${id}/src`,
    targetPath: `/data/${id}/dst`,
    cron: "0 */2 * * *",
    naturalLanguage: "每 2 小时执行一次",
    enabled: true,
    criticalThreshold: 1,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drillEnabled: false,
    drillCron: "",
    drillRestorePath: "/tmp/xirang-drill",
    drillPreVerify: "",
    drillVerify: "",
    drillPostVerify: "",
    drillAutoCleanup: true,
  };
}

function createTask(): TaskRecord {
  return {
    id: 101,
    name: "原始任务名",
    policyName: "每日备份",
    policyId: 1,
    nodeName: "node-1",
    nodeId: 1,
    status: "pending",
    progress: 0,
    startedAt: "2026-03-10 08:00:00",
    rsyncSource: "/old/source",
    rsyncTarget: "/old/target",
    executorType: "rsync",
    revision: "9007199254740993",
    cronSpec: "0 0 * * *",
    speedMbps: 0,
    enabled: true,
  };
}

describe("TaskEditorDialog", () => {
  it("编辑模式只提交实际改动的字段并携带精确 revision 字符串", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn().mockResolvedValue(undefined);
    const onOpenChange = vi.fn();

    render(
      <TaskEditorDialog
        open
        onOpenChange={onOpenChange}
        nodes={[createNode(1, "node-1"), createNode(2, "node-2")]}
        policies={[createPolicy(1, "每日备份"), createPolicy(2, "每小时同步")]}
        onUpdate={onUpdate}
        editingTask={createTask()}
      />
    );

    expect(screen.getByText("编辑任务")).toBeInTheDocument();
    expect(screen.getByLabelText("任务名称")).toHaveValue("原始任务名");
    expect(screen.getByLabelText("目标节点")).toHaveValue("1");
    expect(screen.getByLabelText("关联策略（可选）")).toHaveValue("1");
    expect(screen.getByLabelText("Cron（可选）")).toHaveValue("0 0 * * *");
    expect(screen.getByLabelText("Rsync 源路径（可选）")).toHaveValue("/old/source");
    expect(screen.getByText("/old/target")).toBeInTheDocument();

    await user.clear(screen.getByLabelText("任务名称"));
    await user.type(screen.getByLabelText("任务名称"), "  重命名任务  ");
    await user.selectOptions(screen.getByLabelText("目标节点"), "2");
    await user.selectOptions(screen.getByLabelText("关联策略（可选）"), "2");
    await user.clear(screen.getByLabelText("Cron（可选）"));
    await user.type(screen.getByLabelText("Cron（可选）"), "  0 */4 * * *  ");
    await user.clear(screen.getByLabelText("Rsync 源路径（可选）"));
    await user.type(screen.getByLabelText("Rsync 源路径（可选）"), "  /new/source  ");

    await user.click(screen.getByRole("button", { name: "保存修改" }));

    expect(onUpdate).toHaveBeenCalledWith({
      expectedRevision: "9007199254740993",
      name: "重命名任务",
      nodeId: 2,
      policyId: 2,
      rsyncSource: "/new/source",
      cronSpec: "0 */4 * * *",
    });
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("关闭后以新建模式重新打开时会重置为默认草稿", () => {
    const onUpdate = vi.fn().mockResolvedValue(undefined);
    const onOpenChange = vi.fn();
    const nodes = [createNode(1, "node-1")];
    const policies = [createPolicy(1, "每日备份")];

    const { rerender } = render(
      <TaskEditorDialog
        open
        onOpenChange={onOpenChange}
        nodes={nodes}
        policies={policies}
        onUpdate={onUpdate}
        editingTask={createTask()}
      />
    );

    expect(screen.getByText("编辑任务")).toBeInTheDocument();
    expect(screen.getByLabelText("任务名称")).toHaveValue("原始任务名");

    rerender(
      <TaskEditorDialog
        open={false}
        onOpenChange={onOpenChange}
        nodes={nodes}
        policies={policies}
        onCreate={onUpdate}
        editingTask={null}
      />
    );

    rerender(
      <TaskEditorDialog
        open
        onOpenChange={onOpenChange}
        nodes={nodes}
        policies={policies}
        onCreate={onUpdate}
        editingTask={null}
      />
    );

    expect(screen.getByText("新建任务")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "创建任务" })).toBeInTheDocument();
    expect(screen.getByLabelText("任务名称")).toHaveValue("");
    expect(screen.getByLabelText("目标节点")).toHaveValue("");
    expect(screen.getByLabelText("关联策略（可选）")).toHaveValue("");
    expect(screen.getByLabelText("Cron（可选）")).toHaveValue("");
    expect(screen.getByLabelText("Rsync 源路径（可选）")).toHaveValue("");
    // rsync 模式下目标路径不再是输入框，而是自动生成的只读显示
  });

  it("编辑 Rsync 任务时仅显示安全的恢复点状态摘要", () => {
    render(
      <TaskEditorDialog
        open
        onOpenChange={vi.fn()}
        nodes={[createNode(1, "node-1")]}
        policies={[createPolicy(1, "每日备份")]}
        onUpdate={vi.fn().mockResolvedValue(undefined)}
        editingTask={{
          ...createTask(),
          rsyncPublication: {
            mode: "versioned_full_copy",
            state: "committed",
            reasonCode: "ready",
            capabilityRevision: 8,
            taskRevision: "9007199254740993",
            seedFullCopyRequired: false,
          },
        }}
      />
    );

    expect(screen.getByText("当前恢复点模式")).toBeInTheDocument();
    expect(screen.getByText("版本化完整副本树")).toBeInTheDocument();
    expect(screen.getByText("已提交")).toBeInTheDocument();
  });

  it("编辑受管 Rclone 任务时只显示安全摘要且未改动时只提交 revision", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn().mockResolvedValue(undefined);

    render(
      <TaskEditorDialog
        open
        onOpenChange={vi.fn()}
        nodes={[createNode(1, "node-1")]}
        policies={[createPolicy(1, "每日备份")]}
        onUpdate={onUpdate}
        editingTask={{
          ...createTask(),
          executorType: "rclone",
          rsyncTarget: undefined,
          executorSettings: { bandwidthLimit: "10M", transfers: 4 },
          rclonePublication: {
            mode: "native_object_versions",
            state: "ready",
            reasonCode: "ready",
            taskRevision: "9007199254740993",
            bindingRevision: "7",
            capabilityRevision: "8",
            consistencyClass: "provider_strong",
            hashFidelity: "provider_strong_checksum",
            estimatedReadBytes: "1024",
            apiCostClass: "low",
            storageCostClass: "moderate",
            egressCostClass: "none",
            credentialExpiresAt: "2026-07-17T10:00:00Z",
            encryptionProfile: "sse_kms_cmk",
            kmsKeyStatus: "ready",
            kmsReadKeyCount: 2,
            rollbackLocatorPresent: true,
            rollbackCapability: "preparation_only",
          },
        }}
      />
    );

    expect(screen.getByText("AWS S3 原生版本")).toBeInTheDocument();
    expect(screen.getByText("已就绪")).toBeInTheDocument();
    expect(screen.getByLabelText("执行器类型")).toBeDisabled();
    expect(screen.queryByLabelText("Rclone 远程路径")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("带宽限制（可选）")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("并发传输数（可选）")).not.toBeInTheDocument();
    expect(screen.getByText("受管目标与发布配置只能通过 Rclone 版本化管理修改。")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "保存修改" }));
    expect(onUpdate).toHaveBeenCalledWith({
      expectedRevision: "9007199254740993",
    });
  });

  it("清空 Cron 会显式提交空字符串而不是省略字段", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn().mockResolvedValue(undefined);

    render(
      <TaskEditorDialog
        open
        onOpenChange={vi.fn()}
        nodes={[createNode(1, "node-1")]}
        policies={[createPolicy(1, "每日备份")]}
        onUpdate={onUpdate}
        editingTask={createTask()}
      />
    );

    await user.clear(screen.getByLabelText("Cron（可选）"));
    await user.click(screen.getByRole("button", { name: "保存修改" }));

    expect(onUpdate).toHaveBeenCalledWith({
      expectedRevision: "9007199254740993",
      cronSpec: "",
    });
  });

  it("Restic 编辑器加载安全设置并标明删除保护未验证", () => {
    render(
      <TaskEditorDialog
        open
        onOpenChange={vi.fn()}
        nodes={[createNode(1, "node-1")]}
        policies={[createPolicy(1, "每日备份")]}
        onUpdate={vi.fn().mockResolvedValue(undefined)}
        editingTask={{
          ...createTask(),
          executorType: "restic",
          executorSettings: { excludePatterns: ["*.log", "/tmp"], repositoryVersion: 2 },
          executorSecretsConfigured: { repositoryPassword: true },
        }}
      />
    );

    expect(screen.getByLabelText("仓库格式版本")).toHaveValue("2");
    expect(screen.getByLabelText("排除规则（可选，每行一条）")).toHaveValue("*.log\n/tmp");
    expect(screen.getByText("删除保护：未验证")).toBeInTheDocument();
    expect(screen.getByText(/格式版本 2 不等于不可变/)).toBeInTheDocument();
    expect(screen.queryByText(/Append-Only/)).not.toBeInTheDocument();
    expect(screen.getByText(/已配置仓库密码/)).toBeInTheDocument();
    expect(screen.getByLabelText("仓库密码")).toHaveValue("");
  });

  it("更换仓库密码保留首尾空白而不改变密码", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn().mockResolvedValue(undefined);
    render(
      <TaskEditorDialog
        open
        onOpenChange={vi.fn()}
        nodes={[createNode(1, "node-1")]}
        policies={[]}
        onUpdate={onUpdate}
        editingTask={{ ...createTask(), executorType: "restic" }}
      />
    );
    await user.type(screen.getByLabelText("仓库密码"), "  FAKE replacement password  ");
    await user.click(screen.getByRole("button", { name: "保存修改" }));
    expect(onUpdate).toHaveBeenCalledWith({
      expectedRevision: "9007199254740993",
      executorSecrets: { repositoryPassword: "  FAKE replacement password  " },
    });
  });

  it("409 冲突时保留草稿并提示关闭后重新打开", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn().mockRejectedValue(new ApiError(409, "revision conflict"));
    const onOpenChange = vi.fn();

    render(
      <TaskEditorDialog
        open
        onOpenChange={onOpenChange}
        nodes={[createNode(1, "node-1")]}
        policies={[createPolicy(1, "每日备份")]}
        onUpdate={onUpdate}
        editingTask={createTask()}
      />
    );

    await user.clear(screen.getByLabelText("任务名称"));
    await user.type(screen.getByLabelText("任务名称"), "未保存的草稿名");
    await user.click(screen.getByRole("button", { name: "保存修改" }));

    expect(onOpenChange).not.toHaveBeenCalledWith(false);
    expect(screen.getByText("编辑任务")).toBeInTheDocument();
    expect(screen.getByLabelText("任务名称")).toHaveValue("未保存的草稿名");
    expect(screen.getByText("任务已被更新")).toBeInTheDocument();
    expect(screen.getByText(/关闭后重新打开编辑/)).toBeInTheDocument();
  });
});
