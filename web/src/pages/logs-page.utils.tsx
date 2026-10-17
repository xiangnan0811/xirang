import type { LogEvent, TaskStatus } from "@/types/domain";
import { formatTime } from "@/lib/api/core";

export const selectedNodeStorageKey = "xirang.logs.selected-node";
export const selectedTaskStorageKey = "xirang.logs.selected-task";
export const keywordStorageKey = "xirang.logs.keyword";

const splitByErrorCodeRegex = /(XR-[A-Z]+-\d+)/g;
const singleErrorCodeRegex = /^XR-[A-Z]+-\d+$/;

export function isTerminalTaskStatus(status?: TaskStatus) {
  return status === "success" || status === "failed" || status === "canceled" || status === "warning";
}

export function isActiveTaskStatus(status?: TaskStatus) {
  return status === "running" || status === "retrying";
}

export function highlightErrorCode(message: string) {
  const parts = message.split(splitByErrorCodeRegex);
  return parts.map((part, idx) =>
    singleErrorCodeRegex.test(part) ? (
      <span
        key={`${part}-${idx}`}
        className="rounded border border-destructive/35 bg-destructive/20 px-1 text-destructive"
      >
        {part}
      </span>
    ) : (
      <span key={`${part}-${idx}`}>{part}</span>
    ),
  );
}

export function getLevelClass(level: "info" | "warn" | "error") {
  if (level === "error") {
    return "text-destructive";
  }
  if (level === "warn") {
    return "text-warning";
  }
  return "text-success";
}

export function parseToMillis(log: LogEvent) {
  if (Number.isFinite(log.timestampMs)) return log.timestampMs as number;
  const timestamp = log.timestamp;
  const parsed = Date.parse(timestamp);
  if (Number.isNaN(parsed)) {
    return 0;
  }
  return parsed;
}

export const formatLogTime = formatTime;

export function minLogId(logs: LogEvent[]) {
  let min = Number.MAX_SAFE_INTEGER;
  for (const log of logs) {
    if (log.logId && log.logId < min) {
      min = log.logId;
    }
  }
  return min === Number.MAX_SAFE_INTEGER ? null : min;
}

export type LogsRunScope =
  | { kind: "task" }
  | { kind: "invalid-run" }
  | { kind: "run"; taskId: number; runId: number };

const canonicalPositiveIdRegex = /^[1-9]\d*$/;

function parseCanonicalPositiveId(values: string[]): number | null {
  if (values.length !== 1) return null;
  const raw = values[0];
  if (!canonicalPositiveIdRegex.test(raw)) return null;
  const value = Number(raw);
  return Number.isSafeInteger(value) && value > 0 ? value : null;
}

/**
 * 解析日志页的精确 Run 范围。没有 run 键时保持任务级行为；
 * 一旦出现 run 键，必须恰好各一个显式、规范十进制的安全正整数 task/run，
 * 否则一律视为非法链接，调用方不得回退到持久化任务或最新运行。
 */
export function parseLogsRunScope(params: URLSearchParams): LogsRunScope {
  if (!params.has("run")) return { kind: "task" };
  const taskId = parseCanonicalPositiveId(params.getAll("task"));
  const runId = parseCanonicalPositiveId(params.getAll("run"));
  if (taskId === null || runId === null) return { kind: "invalid-run" };
  return { kind: "run", taskId, runId };
}
