import type {
  BackupFileSourceNode,
  BackupFileSourceSet,
  BackupFileSourceVersion,
} from "@/types/domain";

export interface BackupFileSourceSelectionInput {
  nodes: readonly BackupFileSourceNode[];
  sets: readonly BackupFileSourceSet[];
  versionsBySetId: Readonly<Record<string, readonly BackupFileSourceVersion[]>>;
  selectedNodeId?: number;
  selectedBackupSetId?: string;
  selectedRecoveryPointId?: string;
  nodesComplete?: boolean;
  setsComplete?: boolean;
  versionsComplete?: boolean;
  blocked?: boolean;
}

export interface BackupFileSourceSelection {
  status: "ready" | "empty" | "partial" | "blocked";
  selectedNode: BackupFileSourceNode | null;
  selectedSet: BackupFileSourceSet | null;
  selectedVersion: BackupFileSourceVersion | null;
  sets: BackupFileSourceSet[];
  versions: BackupFileSourceVersion[];
  showSetControl: boolean;
  suggestedBackupSetId?: string;
  mismatch: "node" | "backup_set" | "version" | null;
  primaryLabels: { node: "节点"; set: "备份集"; version: "版本" };
}

export function projectBackupFileSourceSelection(input: BackupFileSourceSelectionInput): BackupFileSourceSelection {
  const nodesComplete = input.nodesComplete !== false;
  const setsComplete = input.setsComplete !== false;
  const versionsComplete = input.versionsComplete !== false;
  const selectedNode = input.selectedNodeId === undefined
    ? null
    : input.nodes.find((item) => item.nodeId === input.selectedNodeId) ?? null;
  const nodeMismatch = nodesComplete && input.selectedNodeId !== undefined && selectedNode === null;
  const sets = selectedNode ? input.sets.filter((item) => item.nodeId === selectedNode.nodeId) : [];
  const implicitSoleSet = setsComplete && input.selectedBackupSetId === undefined && sets.length === 1 ? sets[0] : null;
  const selectedSet = input.selectedBackupSetId === undefined
    ? implicitSoleSet
    : sets.find((item) => item.backupSetId === input.selectedBackupSetId) ?? null;
  const setMismatch = setsComplete && input.selectedBackupSetId !== undefined && selectedSet === null;
  const versions = selectedSet
    ? [...(input.versionsBySetId[selectedSet.backupSetId] ?? [])].sort(compareVersions)
    : [];
  const selectedVersion = input.selectedRecoveryPointId === undefined
    ? null
    : versions.find((item) => item.recoveryPointId === input.selectedRecoveryPointId) ?? null;
  const versionMismatch = versionsComplete && input.selectedRecoveryPointId !== undefined && selectedSet !== null && selectedVersion === null;
  const mismatch = nodeMismatch ? "node" : setMismatch ? "backup_set" : versionMismatch ? "version" : null;
  const partial = [selectedNode?.catalogCoverage, selectedSet?.catalogCoverage, ...versions.map((item) => item.catalogCoverage)]
    .some((status) => status === "partial" || status === "building");

  return {
    status: input.blocked || mismatch !== null ? "blocked" : partial ? "partial" : nodesComplete && input.nodes.length === 0 ? "empty" : "ready",
    selectedNode,
    selectedSet,
    selectedVersion,
    sets,
    versions,
    showSetControl: !setsComplete || sets.length > 1,
    mismatch,
    primaryLabels: { node: "节点", set: "备份集", version: "版本" },
  };
}

export function compareVersions(left: BackupFileSourceVersion, right: BackupFileSourceVersion): number {
  for (const key of ["capturedAt", "committedAt", "createdAt"] as const) {
    const leftValue = left[key];
    const rightValue = right[key];
    if (leftValue === rightValue) continue;
    if (leftValue === null) return 1;
    if (rightValue === null) return -1;
    const leftComparable = comparableUTCInstant(leftValue);
    const rightComparable = comparableUTCInstant(rightValue);
    if (leftComparable === rightComparable) continue;
    return leftComparable > rightComparable ? -1 : 1;
  }
  return right.recoveryPointId.localeCompare(left.recoveryPointId);
}

function comparableUTCInstant(value: string): string {
  const match = /^(.*?)(?:\.(\d{1,9}))?Z$/.exec(value);
  if (!match) return value;
  return `${match[1]}.${(match[2] ?? "").padEnd(9, "0")}Z`;
}
