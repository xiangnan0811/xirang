import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { TaskEditorDialog } from "@/components/task-create-dialog";
import { toast } from "@/components/ui/toast";
import type { NodeRecord, PolicyRecord, TaskRecord } from "@/types/domain";

vi.mock("@/components/ui/toast", () => ({
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
    cronSpec: "0 0 * * *",
    speedMbps: 0,
  };
}

describe("TaskEditorDialog", () => {
  it("编辑模式会回填任务字段，并在保存时转换为 NewTaskInput", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);
    const onOpenChange = vi.fn();

    render(
      <TaskEditorDialog
        open
        onOpenChange={onOpenChange}
        nodes={[createNode(1, "node-1"), createNode(2, "node-2")]}
        policies={[createPolicy(1, "每日备份"), createPolicy(2, "每小时同步")]}
        onSave={onSave}
        editingTask={createTask()}
      />
    );

    expect(screen.getByText("编辑任务")).toBeInTheDocument();
    expect(screen.getByLabelText("任务名称")).toHaveValue("原始任务名");
    expect(screen.getByLabelText("目标节点")).toHaveValue("1");
    expect(screen.getByLabelText("关联策略（可选）")).toHaveValue("1");
    expect(screen.getByLabelText("Cron（可选）")).toHaveValue("0 0 * * *");
    expect(screen.getByLabelText("Rsync 源路径（可选）")).toHaveValue("/old/source");
    expect(screen.getByLabelText("Rsync 目标路径（可选）")).toHaveValue("/old/target");

    await user.clear(screen.getByLabelText("任务名称"));
    await user.type(screen.getByLabelText("任务名称"), "  重命名任务  ");
    await user.selectOptions(screen.getByLabelText("目标节点"), "2");
    await user.selectOptions(screen.getByLabelText("关联策略（可选）"), "2");
    await user.clear(screen.getByLabelText("Cron（可选）"));
    await user.type(screen.getByLabelText("Cron（可选）"), "  0 */4 * * *  ");
    await user.clear(screen.getByLabelText("Rsync 源路径（可选）"));
    await user.type(screen.getByLabelText("Rsync 源路径（可选）"), "  /new/source  ");
    await user.clear(screen.getByLabelText("Rsync 目标路径（可选）"));
    await user.type(screen.getByLabelText("Rsync 目标路径（可选）"), " /new/target ");

    await user.click(screen.getByRole("button", { name: "保存修改" }));

    expect(onSave).toHaveBeenCalledWith({
      name: "重命名任务",
      nodeId: 2,
      policyId: 2,
      dependsOnTaskId: null,
      executorType: "rsync",
      rsyncSource: "/new/source",
      rsyncTarget: "/new/target",
      cronSpec: "0 */4 * * *",
    });
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("关闭后以新建模式重新打开时会重置为默认草稿", () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const onOpenChange = vi.fn();
    const nodes = [createNode(1, "node-1")];
    const policies = [createPolicy(1, "每日备份")];

    const { rerender } = render(
      <TaskEditorDialog
        open
        onOpenChange={onOpenChange}
        nodes={nodes}
        policies={policies}
        onSave={onSave}
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
        onSave={onSave}
        editingTask={null}
      />
    );

    rerender(
      <TaskEditorDialog
        open
        onOpenChange={onOpenChange}
        nodes={nodes}
        policies={policies}
        onSave={onSave}
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
    expect(screen.getByLabelText("Rsync 目标路径（可选）")).toHaveValue("");
  });
});
