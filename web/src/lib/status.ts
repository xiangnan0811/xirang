import i18n from "@/i18n";
import type { AlertSeverity, NodeRecord, NodeStatus, TaskStatus } from "@/types/domain";

/**
 * Outcome shown for a node's latest connection test. `untested` is a neutral "not tested" state, distinct
 * from a failed test; only servers that track config revisions report it (legacy servers keep the raw status).
 */
export type NodeLastTestStatus = NodeStatus | "untested";

export function getNodeStatusMeta(status: NodeLastTestStatus) {
  switch (status) {
    case "online":
      return { label: i18n.t("status.node.online"), variant: "success" as const };
    case "warning":
      return { label: i18n.t("status.node.warning"), variant: "warning" as const };
    case "offline":
      return { label: i18n.t("status.node.offline"), variant: "destructive" as const };
    case "untested":
      return { label: i18n.t("status.node.untested"), variant: "neutral" as const };
    default:
      return { label: i18n.t("status.node.unknown"), variant: "neutral" as const };
  }
}

/**
 * Outcome of the latest recorded connection test. Recorded evidence wins over the raw status, which a legacy
 * node update may overwrite after a real test. A revised server reporting neither evidence nor a test time has
 * never tested the node, so its raw status is a placeholder and the result is `untested`. Without evidence but
 * with a recorded time, the raw status is the legacy outcome; servers without revisions always keep it.
 */
export function getNodeLastTestStatus(
  node: Pick<NodeRecord, "status" | "connectionTest" | "revision" | "lastSeenAt">,
): NodeLastTestStatus {
  if (node.connectionTest) {
    return node.connectionTest.status;
  }
  // Display mappers render a missing time as "-"; neither marks a recorded test.
  if (node.revision && (!node.lastSeenAt || node.lastSeenAt === "-")) {
    return "untested";
  }
  return node.status;
}

/**
 * How the latest recorded connection test relates to the node's current connection settings:
 * - `untested`: no test was recorded;
 * - `current`: the recorded test used the current settings, or the server predates config revisions;
 * - `previous`: the recorded test used settings that have since changed;
 * - `unavailable`: the server tracks revisions but cannot tell which settings the recorded test used.
 */
export type NodeConnectionEvidenceKind = "untested" | "current" | "previous" | "unavailable";

export type NodeConnectionEvidence = {
  kind: NodeConnectionEvidenceKind;
  /** Display time of the recorded test; undefined when none was recorded. */
  testedAt?: string;
  /**
   * Latency of the recorded test; undefined when none was recorded or the test failed. Legacy evidence a
   * revised server cannot attribute keeps only a positive value, since its raw status may be overwritten.
   */
  latencyMs?: number;
};

type EvidenceSource = Pick<NodeRecord, "status" | "lastSeenAt" | "connectionLatencyMs" | "revision" | "connectionTest">;

export function describeNodeConnectionEvidence(node: EvidenceSource): NodeConnectionEvidence {
  // Display mappers render a missing time as "-"; neither marks a recorded test.
  const testedAt = node.lastSeenAt && node.lastSeenAt !== "-" ? node.lastSeenAt : undefined;
  const test = node.connectionTest;
  if (test) {
    // A failed test has no meaningful latency; the stored value is a placeholder.
    const latencyMs = test.status !== "offline" ? node.connectionLatencyMs : undefined;
    return { kind: test.current ? "current" : "previous", testedAt, latencyMs };
  }
  if (!testedAt) {
    return { kind: "untested" };
  }
  if (node.revision) {
    // A legacy update may have overwritten the raw status after a passed test, so it cannot decide whether the
    // latency is real; a positive value can only come from a passed test.
    const latencyMs = node.connectionLatencyMs != null && node.connectionLatencyMs > 0 ? node.connectionLatencyMs : undefined;
    return { kind: "unavailable", testedAt, latencyMs };
  }
  const latencyMs = node.status !== "offline" ? node.connectionLatencyMs : undefined;
  return { kind: "current", testedAt, latencyMs };
}

export function formatNodeConnectionLatency(evidence: NodeConnectionEvidence): string {
  return evidence.latencyMs != null ? `${evidence.latencyMs} ms` : "-";
}

export function getTaskStatusMeta(status: TaskStatus) {
  switch (status) {
    case "running":
      return { label: i18n.t("status.task.running"), variant: "neutral" as const };
    case "pending":
      return { label: i18n.t("status.task.pending"), variant: "neutral" as const };
    case "retrying":
      return { label: i18n.t("status.task.retrying"), variant: "warning" as const };
    case "success":
      return { label: i18n.t("status.task.success"), variant: "success" as const };
    case "failed":
      return { label: i18n.t("status.task.failed"), variant: "destructive" as const };
    case "canceled":
      return { label: i18n.t("status.task.canceled"), variant: "neutral" as const };
    case "warning":
      return { label: i18n.t("status.task.verifyWarning"), variant: "warning" as const };
    default:
      return { label: i18n.t("status.task.unknown"), variant: "neutral" as const };
  }
}

export function getSeverityMeta(severity: AlertSeverity) {
  switch (severity) {
    case "critical":
      return { label: i18n.t("status.alert.critical"), variant: "destructive" as const };
    case "warning":
      return { label: i18n.t("status.alert.warning"), variant: "warning" as const };
    default:
      return { label: i18n.t("status.alert.info"), variant: "neutral" as const };
  }
}
