import type { NodeRecord } from "@/types/domain";

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
  if (value.includes(",") || value.includes("\"") || value.includes("\n")) {
    return `"${value.replace(/"/g, '""')}"`;
  }
  return value;
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

