import { describe, expect, it } from "vitest";

import type {
  BackupFileSourceNode,
  BackupFileSourceRecoveryPoint,
  BackupFileSourceSet,
  BackupFileSourceVersion,
} from "@/types/domain";
import {
  defaultBackupAssetsRouteState,
  gateBackupAssetsBrowseRoute,
  parseBackupAssetsRoute,
  reconcileBackupAssetsSourceRoute,
  resolveBackupAssetsLegacySourceRoute,
  serializeBackupAssetsRoute,
  updateBackupAssetsRoute,
} from "./backup-assets-route-state";

const setId = "a".repeat(32);
const repositoryId = "b".repeat(32);
const pointId = "c".repeat(32);
const parentId = "d".repeat(64);
const entryId = "e".repeat(64);
const node: BackupFileSourceNode = {
  nodeId: 7, displayName: "节点", backupSetCount: 1, retainedVersionCount: 1, latestRetainedAt: null,
  catalogCoverage: "complete", browseState: "browsable", unavailableReason: null,
};
const set: BackupFileSourceSet = {
  backupSetId: setId, nodeId: 7, displayLabel: "每日备份", lineageKind: "task", versionCount: 1, latestRetainedAt: null,
  catalogCoverage: "complete", browseState: "browsable", unavailableReason: null,
};
const version: BackupFileSourceVersion = {
  recoveryPointId: pointId, repositoryId, producingTaskId: 9, capturedAt: null, committedAt: null,
  createdAt: "2026-08-27T00:00:00.000Z", lifecycleState: "committed", catalogCoverage: "complete",
  browseState: "browsable", unavailableReason: null,
  contentAvailability: { available: false, reason: { code: "range_unavailable", params: {} } }, entryCount: 1,
  logicalBytes: 1, permissions: { list: true, preview: false, download: false },
};
const resolution: BackupFileSourceRecoveryPoint = {
  nodeId: node.nodeId,
  backupSetId: set.backupSetId,
  recoveryPointId: pointId,
  repositoryId,
  producingTaskId: 9,
  browseState: "browsable",
  unavailableReason: null,
};

describe("backup file source route state", () => {
  it("round trips typed source ids while preserving opaque legacy context", () => {
    const href = `/app/backups/data?nodeId=7&backupSetId=${setId}&repositoryId=${repositoryId}&taskId=9&recoveryPointId=${pointId}&parentEntryId=${parentId}&entryId=${entryId}`;
    const parsed = parseBackupAssetsRoute("/app/backups/data", href.slice(href.indexOf("?")));
    expect(parsed.status).toBe("valid");
    if (parsed.status === "valid") {
      expect(parsed.state).toMatchObject({ nodeId: 7, backupSetId: setId, repositoryId, taskId: 9, recoveryPointId: pointId });
      expect(serializeBackupAssetsRoute(parsed.state)).toBe(href);
    }
  });

  it("clears descendants on source changes but preserves explicitly supplied exact version context", () => {
    const source = { ...defaultBackupAssetsRouteState("data"), nodeId: 7, backupSetId: setId, repositoryId, taskId: 9, recoveryPointId: pointId, parentEntryId: parentId, entryId };
    const nodeChanged = updateBackupAssetsRoute(source, { nodeId: 8 });
    expect(nodeChanged.status === "valid" && nodeChanged.state).toMatchObject({ nodeId: 8 });
    if (nodeChanged.status === "valid") {
      expect(nodeChanged.state.backupSetId).toBeUndefined();
      expect(nodeChanged.state.recoveryPointId).toBeUndefined();
      expect(nodeChanged.state.entryId).toBeUndefined();
    }
    const nextSet = "f".repeat(32);
    const exact = updateBackupAssetsRoute(source, {
      backupSetId: nextSet, repositoryId: "1".repeat(32), taskId: 11, recoveryPointId: "2".repeat(32),
    });
    expect(exact.status === "valid" && exact.state).toMatchObject({ backupSetId: nextSet, taskId: 11, recoveryPointId: "2".repeat(32) });
    if (exact.status === "valid") expect(exact.state.entryId).toBeUndefined();
  });

  it("clears a mismatched branch and never guesses a replacement", () => {
    const source = { ...defaultBackupAssetsRouteState("data"), nodeId: 7, backupSetId: "f".repeat(32), repositoryId, taskId: 9, recoveryPointId: pointId, entryId };
    expect(reconcileBackupAssetsSourceRoute(source, [node], [set], [version])).toEqual({
      backupSetId: undefined, repositoryId: undefined, taskId: undefined, recoveryPointId: undefined,
      parentEntryId: undefined, entryId: undefined, exportJobId: undefined,
    });
  });

  it("patches an exact legacy recovery point into the authorized source hierarchy", () => {
    const source = { ...defaultBackupAssetsRouteState("data"), taskId: 9, repositoryId, recoveryPointId: pointId, entryId };

    expect(resolveBackupAssetsLegacySourceRoute(source, resolution)).toEqual({
      nodeId: 7,
      backupSetId: setId,
      repositoryId,
      taskId: 9,
      recoveryPointId: pointId,
    });
  });

  it.each(["indexing", "unavailable"] as const)(
    "removes active browsing coordinates while a retained version is %s",
    (browseState) => {
      const source = {
        ...defaultBackupAssetsRouteState("data"),
        nodeId: 7,
        backupSetId: setId,
        repositoryId,
        taskId: 9,
        recoveryPointId: pointId,
        parentEntryId: parentId,
        entryId,
        exportJobId: "f".repeat(32),
      };

      expect(gateBackupAssetsBrowseRoute(source, browseState)).toEqual({
        ...source,
        repositoryId: undefined,
        taskId: undefined,
        recoveryPointId: undefined,
        parentEntryId: undefined,
        entryId: undefined,
        exportJobId: undefined,
      });
    },
  );

  it("clears incompatible legacy descendants on source-coordinate mismatch", () => {
    const source = {
      ...defaultBackupAssetsRouteState("data"),
      nodeId: 8,
      taskId: 9,
      repositoryId,
      recoveryPointId: pointId,
      parentEntryId: parentId,
      entryId,
    };

    expect(resolveBackupAssetsLegacySourceRoute(source, resolution)).toEqual({
      backupSetId: undefined,
      repositoryId: undefined,
      taskId: undefined,
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
      exportJobId: undefined,
    });
  });

  it("preserves legacy repository/task context while clearing a mismatched recovery-point descendant", () => {
    const source = {
      ...defaultBackupAssetsRouteState("data"),
      taskId: 9,
      repositoryId: "f".repeat(32),
      recoveryPointId: pointId,
      parentEntryId: parentId,
      entryId,
    };

    expect(resolveBackupAssetsLegacySourceRoute(source, resolution)).toEqual({
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
      exportJobId: undefined,
    });
  });
});
