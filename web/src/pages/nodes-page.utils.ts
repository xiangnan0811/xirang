import type { NodeRecord, SSHKeyRecord } from "@/types/domain";

export const nodeStatusPriority: Record<NodeRecord["status"], number> = {
  offline: 3,
  warning: 2,
  online: 1,
};

export function parseDateTime(input: string) {
  const value = Date.parse(input);
  return Number.isNaN(value) ? 0 : value;
}

export type NodesViewProps = {
  loading: boolean;
  requestFailed?: boolean;
  inventoryEmpty: boolean;
  sortedNodes: NodeRecord[];
  sshKeys: SSHKeyRecord[];
  selectedNodeSet: Set<number>;
  selectedNodeIds: number[];
  allVisibleSelected: boolean;
  testingNodeId: number | null;
  doctorNodeId?: number | null;
  triggeringNodeId: number | null;
  toggleNodeSelection: (id: number, checked: boolean) => void;
  toggleSelectAllVisible: (checked: boolean) => void;
  handleBulkDelete: () => void;
  resetFilters: () => void;
  openCreateDialog: () => void;
  openEditDialog: (node: NodeRecord, opener?: EventTarget | null) => void;
  onTestNode: (node: NodeRecord) => void;
  onOpenDoctor?: (node: NodeRecord) => void;
  onDeleteNode: (node: NodeRecord) => void;
  handleTriggerBackup: (id: number, name: string) => void;
  onOpenTerminal?: (node: NodeRecord) => void;
  onOpenFileBrowser?: (node: NodeRecord, opener?: EventTarget | null) => void;
  onEmergencyBackup?: (nodeId: number, nodeName: string) => void;
  onMigrate?: (node: NodeRecord) => void;
  emergencyNodeId?: number | null;
  isAdmin: boolean;
  canOperateNodes: boolean;
  canBrowseNodeFiles: boolean;
};
