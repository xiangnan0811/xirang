import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { runAxe } from "@/test/a11y-helpers";
import type { BackupFileSourceNode, BackupFileSourceSet, BackupFileSourceVersion } from "@/types/domain";
import { BackupFileSourceControls } from "./backup-file-source-controls";

const node: BackupFileSourceNode = { nodeId: 7, displayName: "数据库节点", backupSetCount: 1, latestRetainedAt: null, catalogCoverage: "complete" };
const set: BackupFileSourceSet = { backupSetId: "a".repeat(32), nodeId: 7, displayLabel: "每日备份", lineageKind: "task", versionCount: 1, latestRetainedAt: null, catalogCoverage: "complete" };
const version: BackupFileSourceVersion = {
  recoveryPointId: "b".repeat(32), repositoryId: "c".repeat(32), producingTaskId: 9,
  capturedAt: "2026-08-27T00:00:00.000Z", committedAt: null, createdAt: "2026-08-27T00:00:00.000Z",
  lifecycleState: "committed", catalogCoverage: "complete", contentAvailability: { available: false, reason: { code: "range_unavailable", params: {} } },
  entryCount: 1, logicalBytes: 1, permissions: { list: true, preview: false, download: false },
};
const pagingProps = {
  hasMoreNodes: false,
  hasMoreSets: false,
  hasMoreVersions: false,
  loadingMoreNodes: false,
  loadingMoreSets: false,
  loadingMoreVersions: false,
  onLoadMoreNodes: vi.fn(),
  onLoadMoreSets: vi.fn(),
  onLoadMoreVersions: vi.fn(),
};

describe("BackupFileSourceControls", () => {
  it("keeps a sole set implicit and emits exact opaque version context", async () => {
    const onSelectVersion = vi.fn();
    render(<BackupFileSourceControls
      status="ready" nodes={[node]} sets={[set]} versions={[version]} selectedNodeId={7}
      selectedBackupSetId={undefined} selectedRecoveryPointId={undefined}
      onSelectNode={vi.fn()} onSelectSet={vi.fn()} onSelectVersion={onSelectVersion}
      {...pagingProps}
    />);
    expect(screen.queryByLabelText(/Backup set|备份集/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Repository|Provider|仓库|提供商/i)).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText(/Version|版本/), version.recoveryPointId);
    expect(onSelectVersion).toHaveBeenCalledWith(version, set.backupSetId);
  });

  it("shows distinct partial and blocked source states", () => {
    const props = { nodes: [node], sets: [set], versions: [], selectedNodeId: 7, selectedBackupSetId: undefined,
      selectedRecoveryPointId: undefined, onSelectNode: vi.fn(), onSelectSet: vi.fn(), onSelectVersion: vi.fn(), ...pagingProps };
    const { rerender } = render(<BackupFileSourceControls {...props} status="partial" />);
    expect(screen.getByRole("status")).toHaveTextContent(/partial|不完整/i);
    rerender(<BackupFileSourceControls {...props} status="blocked" />);
    expect(screen.getByRole("alert")).toHaveTextContent(/unavailable|不可用/i);
  });

  it("keeps every cursor chain reachable and does not infer a sole set from an incomplete page", async () => {
    const onLoadMoreNodes = vi.fn();
    const onLoadMoreSets = vi.fn();
    const onLoadMoreVersions = vi.fn();
    render(<BackupFileSourceControls
      status="ready" nodes={[node]} sets={[set]} versions={[version]} selectedNodeId={7}
      selectedBackupSetId={undefined} selectedRecoveryPointId={undefined}
      onSelectNode={vi.fn()} onSelectSet={vi.fn()} onSelectVersion={vi.fn()}
      hasMoreNodes hasMoreSets hasMoreVersions loadingMoreNodes={false} loadingMoreSets={false} loadingMoreVersions={false}
      onLoadMoreNodes={onLoadMoreNodes} onLoadMoreSets={onLoadMoreSets} onLoadMoreVersions={onLoadMoreVersions}
    />);

    expect(screen.getByLabelText(/Backup set|备份集/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /more nodes|更多节点/i }));
    await userEvent.click(screen.getByRole("button", { name: /more backup sets|更多备份集/i }));
    await userEvent.click(screen.getByRole("button", { name: /more versions|更多版本/i }));
    expect(onLoadMoreNodes).toHaveBeenCalledOnce();
    expect(onLoadMoreSets).toHaveBeenCalledOnce();
    expect(onLoadMoreVersions).toHaveBeenCalledOnce();
  });

  it("announces loading, exhausted empty, and permission-denied states distinctly", () => {
    const props = {
      nodes: [] as BackupFileSourceNode[], sets: [] as BackupFileSourceSet[], versions: [] as BackupFileSourceVersion[],
      selectedNodeId: undefined, selectedBackupSetId: undefined, selectedRecoveryPointId: undefined,
      onSelectNode: vi.fn(), onSelectSet: vi.fn(), onSelectVersion: vi.fn(), ...pagingProps,
    };
    const { rerender } = render(<BackupFileSourceControls {...props} status="loading" />);
    expect(screen.getByRole("status")).toHaveTextContent(/loading|加载/i);
    rerender(<BackupFileSourceControls {...props} status="empty" />);
    expect(screen.getByRole("status")).toHaveTextContent(/no authorized nodes|没有可访问.*节点/i);
    rerender(<BackupFileSourceControls {...props} status="permission_denied" />);
    expect(screen.getByRole("alert")).toHaveTextContent(/permission|权限/i);
  });

  it("keeps long Chinese and English labels, deterministic control order, mobile touch targets, and axe semantics", async () => {
    const longChineseName = "上海归档数据库节点（跨区域只读副本与季度保留策略）";
    const longEnglishName = "Long English disaster recovery node with quarterly retained snapshots";
    const secondSet = { ...set, backupSetId: "d".repeat(32), displayLabel: longEnglishName };
    const { container } = render(
      <BackupFileSourceControls
        status="ready"
        nodes={[{ ...node, displayName: longChineseName }]}
        sets={[set, secondSet]}
        versions={[version]}
        selectedNodeId={7}
        selectedBackupSetId={secondSet.backupSetId}
        selectedRecoveryPointId={version.recoveryPointId}
        onSelectNode={vi.fn()}
        onSelectSet={vi.fn()}
        onSelectVersion={vi.fn()}
        {...pagingProps}
      />,
    );

    const controls = screen.getAllByRole("combobox");
    expect(controls.map((control) => control.getAttribute("aria-label"))).toEqual(["节点", "备份集", "版本"]);
    expect(controls.every((control) => control.classList.contains("h-11"))).toBe(true);
    expect(controls.every((control) => control.classList.contains("lg:h-9"))).toBe(true);
    expect(controls.every((control) => control.classList.contains("touch-target"))).toBe(true);
    expect(screen.getByRole("option", { name: longChineseName })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: longEnglishName })).toBeInTheDocument();
    expect(await runAxe(container)).toHaveNoViolations();
  });
});
