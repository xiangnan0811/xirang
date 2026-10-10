import type { AlertRecord } from "@/types/domain";

/**
 * Producer categories a quick silence may name.
 * An instance code is exactly `category + "-" + ASCII digits`.
 * This set is not the settings-page category list.
 */
const QUICK_SILENCE_CATEGORIES = [
  "XR-EXEC",
  "XR-VRFY",
  "XR-NODE-EXPIRY",
  "XR-RETN",
  "XR-INTG",
  "XR-DRILL-drill_sandbox_unreachable",
  "XR-DRILL-drill_verify_failed",
  "XR-DRILL-drill_restore_failed",
  "XR-SNAPSHOT-CHURN",
  "XR-SNAPSHOT-RANSOM",
] as const;

const quickSilenceCategories = new Set<string>(QUICK_SILENCE_CATEGORIES);

export type QuickSilenceMatch = {
  nodeId: number;
  category: string;
};

export type QuickSilenceSelection =
  | { kind: "supported"; match: QuickSilenceMatch }
  | { kind: "unsupported"; reason: "platform" | "unknown-node" | "unsupported-source" };

/** True only for an allowlisted category literal, not an instance code or a prefix. */
export function isQuickSilenceCategory(category: unknown): boolean {
  return typeof category === "string" && quickSilenceCategories.has(category);
}

/**
 * True for a positive safe integer node id.
 * Zero is the platform sentinel and is not a quick-silence node.
 */
export function isQuickSilenceNodeId(nodeId: unknown): boolean {
  return typeof nodeId === "number" && Number.isSafeInteger(nodeId) && nodeId > 0;
}

/**
 * Choose a node and category quick silence may submit.
 * Platform node 0, an unreliable node, an SLO alert, or an unrecognized code
 * stays unsupported. Failure does not invent a null node or an empty category.
 */
export function selectQuickSilenceMatch(
  alert: Pick<AlertRecord, "nodeId" | "errorCode" | "sloId">,
): QuickSilenceSelection {
  if (alert.nodeId === 0) {
    return { kind: "unsupported", reason: "platform" };
  }
  if (!isQuickSilenceNodeId(alert.nodeId)) {
    return { kind: "unsupported", reason: "unknown-node" };
  }
  if (alert.sloId !== undefined && alert.sloId !== null) {
    return { kind: "unsupported", reason: "unsupported-source" };
  }
  const category = categoryFromInstanceCode(alert.errorCode);
  if (category === null) {
    return { kind: "unsupported", reason: "unsupported-source" };
  }
  return { kind: "supported", match: { nodeId: alert.nodeId, category } };
}

function categoryFromInstanceCode(errorCode: unknown): string | null {
  if (typeof errorCode !== "string") {
    return null;
  }
  const separator = errorCode.lastIndexOf("-");
  if (separator <= 0) {
    return null;
  }
  const category = errorCode.slice(0, separator);
  const instance = errorCode.slice(separator + 1);
  if (!isAsciiDigits(instance) || !isQuickSilenceCategory(category)) {
    return null;
  }
  return category;
}

function isAsciiDigits(value: string): boolean {
  if (value.length === 0) {
    return false;
  }
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 48 || code > 57) {
      return false;
    }
  }
  return true;
}
