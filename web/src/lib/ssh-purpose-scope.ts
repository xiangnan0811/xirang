/** Historical SSH purposes. They stay parsable and editable, and are not offered for new keys. */
export const RETIRED_SSH_PURPOSES = ["probe", "node_logs"] as const;

/**
 * Purposes a new restriction may suggest.
 * Retired tokens are absent on purpose: suggesting them would create a new execution scope.
 */
export const SSH_PURPOSE_SUGGESTIONS = [
  "terminal",
  "task_command",
  "task_backup",
  "task_restore",
  "node_test",
  "batch_command",
  "file_browser",
  "snapshot",
  "node_migration",
] as const;

const retiredPurposes = new Set<string>(RETIRED_SSH_PURPOSES);

export function splitSSHPurposes(raw: string | null | undefined): string[] {
  const trimmed = String(raw ?? "").trim();
  if (!trimmed || trimmed === "[]" || trimmed === "null") {
    return [];
  }

  let parts: string[];
  if (trimmed.startsWith("[")) {
    try {
      const parsed: unknown = JSON.parse(trimmed);
      parts = Array.isArray(parsed) ? parsed.map((item) => String(item)) : trimmed.split(/[,，]+/);
    } catch {
      parts = trimmed.split(/[,，]+/);
    }
  } else {
    parts = trimmed.split(/[,，]+/);
  }

  const seen = new Set<string>();
  const purposes: string[] = [];
  for (const part of parts) {
    const token = part.trim().toLowerCase();
    if (!token || seen.has(token)) {
      continue;
    }
    seen.add(token);
    purposes.push(token);
  }
  return purposes;
}

/**
 * Round-trip stored purposes, including historical probe and node_logs.
 * A retired-only list stays non-empty. Empty means every current purpose.
 */
export function preserveSSHPurposeScope(raw: string | null | undefined): string {
  return splitSSHPurposes(raw).join(",");
}

export function retiredSSHPurposesIn(raw: string | null | undefined): string[] {
  return splitSSHPurposes(raw).filter((purpose) => retiredPurposes.has(purpose));
}

/**
 * Same predicate as backend IsBroadScope: empty purposes, or no node-id and tag limits.
 * Retired-only purposes do not widen authorization, and they do not hide a broad node scope.
 */
export function sshKeyIsBroadScope(input: {
  allowedPurposes?: string | null;
  allowedNodeIds?: string | null;
  allowedNodeTags?: string | null;
}): boolean {
  const purposes = preserveSSHPurposeScope(input.allowedPurposes);
  const nodeIds = String(input.allowedNodeIds ?? "").trim();
  const nodeTags = String(input.allowedNodeTags ?? "").trim();
  return purposes.length === 0 || (nodeIds.length === 0 && nodeTags.length === 0);
}
