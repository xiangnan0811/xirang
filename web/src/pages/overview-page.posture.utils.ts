import type { BackupConfidenceData, BackupHealthData } from "@/types/domain";

function parseValidTimestamp(value?: string | null): number | null {
  if (!value || !value.trim()) {
    return null;
  }
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : null;
}

/**
 * Picks the latest verified backup completion timestamp solely from backup-health,
 * which aggregates across all visible nodes. Never mixes policy-scoped confidence
 * timestamps or fabricates a timestamp when backup-health failed or returned null.
 */
export function getLatestVerifiedBackupTime(
  health: BackupHealthData | null,
): string | null {
  if (!health) {
    return null;
  }
  if (health.latestVerifiedBackupAt !== undefined) {
    return parseValidTimestamp(health.latestVerifiedBackupAt) !== null
      ? health.latestVerifiedBackupAt
      : null;
  }

  let latestMs = -Infinity;
  let latestIso: string | null = null;
  for (const staleNode of health.staleNodes) {
    const ms = parseValidTimestamp(staleNode.lastBackupAt);
    if (ms !== null && ms > latestMs && staleNode.lastBackupAt) {
      latestMs = ms;
      latestIso = staleNode.lastBackupAt;
    }
  }
  return latestIso;
}

export function isConfidenceUnverified(confidence: BackupConfidenceData): boolean {
  const { total, healthy, warning, atRisk } = confidence.summary;
  return total === 0 || (healthy === 0 && warning === 0 && atRisk === 0);
}

export function getPostureGeneratedAt(
  confidence: BackupConfidenceData | null,
  health: BackupHealthData | null,
): string | null {
  if (confidence?.generatedAt && parseValidTimestamp(confidence.generatedAt) !== null) {
    return confidence.generatedAt;
  }
  if (health?.generatedAt && parseValidTimestamp(health.generatedAt) !== null) {
    return health.generatedAt;
  }
  return null;
}
