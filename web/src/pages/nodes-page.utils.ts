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

export type CSVNodeRow = {
  name: string;
  host: string;
  username: string;
  port: number;
  tags: string;
};

export function escapeCSVValue(value: string): string {
  // Prevent spreadsheet formula injection: detect formula prefixes (=, +, -, @, tab, CR)
  // even after leading whitespace or control characters that spreadsheets may strip.
  let safe = value;
  if (/^[\s\x00-\x1f]*[=+\-@\t\r]/.test(safe)) {
    safe = `'${safe}`;
  }
  if (safe.includes(",") || safe.includes("\"") || safe.includes("\n") || safe !== value) {
    return `"${safe.replace(/"/g, '""')}"`;
  }
  return safe;
}

export function parseCSVRows(content: string): CSVNodeRow[] {
  const lines = content
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  if (!lines.length) {
    return [];
  }

  const first = lines[0].toLowerCase();
  const hasHeader = first.includes("name") && first.includes("host");
  const body = hasHeader ? lines.slice(1) : lines;

  return body
    .map((line) => {
      const [name = "", host = "", username = "root", portRaw = "22", tags = ""] = line
        .split(",")
        .map((one) => one.trim());
      const port = Number(portRaw);
      if (!name || !host) {
        return null;
      }
      return {
        name,
        host,
        username: username || "root",
        port: Number.isFinite(port) && port > 0 ? port : 22,
        tags,
      } as CSVNodeRow;
    })
    .filter((item): item is CSVNodeRow => Boolean(item));
}

export function getDiskBarToneClass(percent: number) {
  if (percent < 20) {
    return "bg-destructive";
  }
  if (percent < 40) {
    return "bg-warning";
  }
  return "bg-success";
}

export type NodesViewProps = {
  loading: boolean;
  sortedNodes: NodeRecord[];
  sshKeys: SSHKeyRecord[];
  selectedNodeSet: Set<number>;
  selectedNodeId: number | null;
  selectedNodeIds: number[];
  allVisibleSelected: boolean;
  testingNodeId: number | null;
  triggeringNodeId: number | null;
  toggleNodeSelection: (id: number, checked: boolean) => void;
  toggleSelectAllVisible: (checked: boolean) => void;
  setSelectedNodeId: (id: number) => void;
  handleBulkDelete: () => void;
  resetFilters: () => void;
  openCreateDialog: () => void;
  openEditDialog: (node: NodeRecord) => void;
  onTestNode: (node: NodeRecord) => void;
  onDeleteNode: (node: NodeRecord) => void;
  handleTriggerBackup: (id: number, name: string) => void;
};
