import type { AlertRecord } from "@/types/domain";

/**
 * Build the href that jumps to the metrics tab for an alert, centred on a
 * ±15-minute window around the trigger timestamp.
 */
export function buildAlertJumpHref(alert: Pick<AlertRecord, "nodeId" | "triggeredAt">): string {
  const rawTs = (alert as AlertRecord).triggeredAt ?? "";
  const ts = new Date(rawTs);
  if (Number.isNaN(ts.getTime())) {
    return `/app/nodes/${alert.nodeId}?tab=metrics`;
  }
  const from = new Date(ts.getTime() - 15 * 60 * 1000).toISOString();
  const to = new Date(ts.getTime() + 15 * 60 * 1000).toISOString();
  return `/app/nodes/${alert.nodeId}?tab=metrics&from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`;
}
