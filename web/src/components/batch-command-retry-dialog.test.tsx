import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLanguage } from "@/i18n";
import { BatchCommandRetryDialog, type BatchRetryRowSnapshot } from "./batch-command-retry-dialog";

const rows: BatchRetryRowSnapshot[] = [
  { taskId: 7, runId: 9, nodeId: 3, nodeName: "node-a", failureKind: "execution" },
  { taskId: 8, runId: 0, nodeId: 5, nodeName: "node-b", failureKind: "dispatch" },
];

function renderRetryDialog(overrides: Partial<{
  rows: readonly BatchRetryRowSnapshot[];
  saving: boolean;
  retain: boolean;
  error: string | null;
}> = {}) {
  const onOpenChange = vi.fn();
  const onConfirm = vi.fn();
  render(
    <BatchCommandRetryDialog
      open
      onOpenChange={onOpenChange}
      rows={overrides.rows ?? rows}
      sourceBatchId={"C".repeat(26)}
      retain={overrides.retain ?? true}
      saving={overrides.saving ?? false}
      error={overrides.error ?? null}
      onConfirm={onConfirm}
    />,
  );
  return { onOpenChange, onConfirm };
}

describe("BatchCommandRetryDialog", () => {
  beforeEach(async () => {
    await setLanguage("zh");
  });

  afterEach(async () => {
    cleanup();
    await setLanguage("zh");
  });

  it("shows every selected snapshot, inherited retain, and the partial-effect warning", async () => {
    renderRetryDialog();

    expect(screen.getByRole("dialog", { name: "确认重试命令" })).toBeInTheDocument();
    expect(screen.getByText("命令可能已产生部分效果，再执行会产生新的操作")).toBeInTheDocument();
    expect(screen.getByText("C".repeat(26))).toBeInTheDocument();
    expect(screen.getByText("启用")).toBeInTheDocument();
    expect(screen.getByText(/node-a（节点 ID：3），任务 7，运行 9，执行失败/)).toBeInTheDocument();
    expect(screen.getByText(/node-b（节点 ID：5），任务 8，运行 0，未派发失败/)).toBeInTheDocument();

    await setLanguage("en");
    expect(screen.getByText("The command may already have taken partial effect. Running it again creates a new operation.")).toBeInTheDocument();
    expect(screen.getByText("Enabled")).toBeInTheDocument();
  });

  it("requires the selected node count before confirming multiple nodes", async () => {
    const user = userEvent.setup();
    const { onConfirm } = renderRetryDialog();

    await user.click(screen.getByRole("button", { name: "确认重试" }));
    expect(onConfirm).not.toHaveBeenCalled();
    expect(screen.getByText("请输入 2 以确认全部节点。")).toBeInTheDocument();

    await user.type(screen.getByRole("textbox"), "2");
    await user.click(screen.getByRole("button", { name: "确认重试" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("confirms a single node without a count and blocks close while saving", async () => {
    const user = userEvent.setup();
    const singleRow = rows[0];
    if (!singleRow) throw new Error("missing retry row");
    const single = renderRetryDialog({ rows: [singleRow] });
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认重试" }));
    expect(single.onConfirm).toHaveBeenCalledTimes(1);
    cleanup();

    const saving = renderRetryDialog({ rows: [singleRow], saving: true });
    await user.click(screen.getByRole("button", { name: "Close" }));
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(saving.onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByText("正在提交重试，请保持窗口打开。关闭不会取消已经发出的请求。")).toBeInTheDocument();
  });
});
