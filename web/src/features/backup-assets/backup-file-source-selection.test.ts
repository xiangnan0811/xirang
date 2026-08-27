import { describe, expect, it } from "vitest";

import type { BackupFileSourceNode, BackupFileSourceSet, BackupFileSourceVersion } from "@/types/domain";
import { projectBackupFileSourceSelection } from "./backup-file-source-selection";

const node: BackupFileSourceNode = {
  nodeId: 7,
  displayName: "数据库节点",
  backupSetCount: 1,
  latestRetainedAt: "2026-08-27T00:00:00.000Z",
  catalogCoverage: "complete",
};
const taskSet: BackupFileSourceSet = {
  backupSetId: "a".repeat(32), nodeId: 7, displayLabel: "每日备份", lineageKind: "task",
  versionCount: 2, latestRetainedAt: "2026-08-27T00:00:00.000Z", catalogCoverage: "complete",
};
const importedSet: BackupFileSourceSet = {
  backupSetId: "b".repeat(32), nodeId: 7, displayLabel: "历史导入", lineageKind: "imported",
  versionCount: 1, latestRetainedAt: null, catalogCoverage: "partial",
};
const version = (id: string, capturedAt: string | null, createdAt: string): BackupFileSourceVersion => ({
  recoveryPointId: id.repeat(32), repositoryId: "f".repeat(32), capturedAt, committedAt: null, createdAt,
  lifecycleState: "committed", catalogCoverage: "complete",
  contentAvailability: { available: false, reason: { code: "range_unavailable", params: {} } },
  entryCount: 1, logicalBytes: 2, permissions: { list: true, preview: false, download: false },
});

describe("backup file source selection projection", () => {
  it("selects and hides a sole set without inventing a durable route value", () => {
    const projected = projectBackupFileSourceSelection({
      nodes: [node], sets: [taskSet], versionsBySetId: { [taskSet.backupSetId]: [] }, selectedNodeId: 7,
    });
    expect(projected.selectedSet?.backupSetId).toBe(taskSet.backupSetId);
    expect(projected.showSetControl).toBe(false);
    expect(projected.suggestedBackupSetId).toBeUndefined();
  });

  it("does not treat one loaded set as the sole set before cursor exhaustion", () => {
    const projected = projectBackupFileSourceSelection({
      nodes: [node], sets: [taskSet], versionsBySetId: {}, selectedNodeId: 7, setsComplete: false,
    });

    expect(projected.selectedSet).toBeNull();
    expect(projected.showSetControl).toBe(true);
    expect(projected.mismatch).toBeNull();
  });

  it("does not report an authoritative empty state before node cursor exhaustion", () => {
    expect(projectBackupFileSourceSelection({
      nodes: [], sets: [], versionsBySetId: {}, nodesComplete: false,
    }).status).toBe("ready");
  });

  it("shows multiple sets and keeps imported taskless versions isolated by set identity", () => {
    const importedVersion = version("2", null, "2026-08-25T00:00:00.000Z");
    const projected = projectBackupFileSourceSelection({
      nodes: [node], sets: [taskSet, importedSet],
      versionsBySetId: { [taskSet.backupSetId]: [version("1", null, "2026-08-26T00:00:00.000Z")], [importedSet.backupSetId]: [importedVersion] },
      selectedNodeId: 7, selectedBackupSetId: importedSet.backupSetId,
    });
    expect(projected.showSetControl).toBe(true);
    expect(projected.versions).toEqual([importedVersion]);
    expect(projected.selectedSet?.lineageKind).toBe("imported");
  });

  it("sorts versions deterministically by retained time then opaque id", () => {
    const older = version("1", "2026-08-26T00:00:00.000Z", "2026-08-20T00:00:00.000Z");
    const newerA = version("2", "2026-08-27T00:00:00.000Z", "2026-08-20T00:00:00.000Z");
    const newerB = version("3", "2026-08-27T00:00:00.000Z", "2026-08-20T00:00:00.000Z");
    const projected = projectBackupFileSourceSelection({
      nodes: [node], sets: [taskSet], versionsBySetId: { [taskSet.backupSetId]: [older, newerA, newerB] }, selectedNodeId: 7,
    });
    expect(projected.versions.map((item) => item.recoveryPointId)).toEqual([
      newerB.recoveryPointId, newerA.recoveryPointId, older.recoveryPointId,
    ]);
  });

  it("orders UTC instants without discarding sub-millisecond precision", () => {
    const earlier = version("1", "2026-08-27T00:00:00.1Z", "2026-08-20T00:00:00.000Z");
    const later = version("2", "2026-08-27T00:00:00.11Z", "2026-08-20T00:00:00.000Z");

    const projected = projectBackupFileSourceSelection({
      nodes: [node], sets: [taskSet], versionsBySetId: { [taskSet.backupSetId]: [earlier, later] }, selectedNodeId: 7,
    });

    expect(projected.versions).toEqual([later, earlier]);
  });

  it("distinguishes blocked and partial source states and clears mismatches without replacement", () => {
    expect(projectBackupFileSourceSelection({ nodes: [], sets: [], versionsBySetId: {}, blocked: true }).status).toBe("blocked");
    expect(projectBackupFileSourceSelection({
      nodes: [{ ...node, catalogCoverage: "partial" }], sets: [], versionsBySetId: {}, selectedNodeId: 7,
    }).status).toBe("partial");
    const mismatch = projectBackupFileSourceSelection({
      nodes: [node], sets: [taskSet], versionsBySetId: {}, selectedNodeId: 7, selectedBackupSetId: "9".repeat(32),
    });
    expect(mismatch.mismatch).toBe("backup_set");
    expect(mismatch.selectedSet).toBeNull();
    expect(mismatch.suggestedBackupSetId).toBeUndefined();
  });

  it("uses user-facing source labels without internal storage vocabulary", () => {
    const labels = projectBackupFileSourceSelection({ nodes: [], sets: [], versionsBySetId: {} }).primaryLabels;
    expect(labels).toEqual({ node: "节点", set: "备份集", version: "版本" });
    expect(Object.values(labels).join(" ")).not.toMatch(/repository|provider|仓库|提供商/i);
  });
});
