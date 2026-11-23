import type { BackupConfidenceData, BackupHealthData } from "@/types/domain";

function parseValidTimestamp(value?: string | null): number | null {
  if (!value || !value.trim()) {
    return null;
  }
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : null;
}

/**
 * Picks the latest verified backup completion timestamp from backup-confidence
 * targets/verified backup evidence and backup-health stale-node facts.
 * Never fabricates a timestamp from unverified task runs or node heartbeats.
 */
export function getLatestVerifiedBackupTime(
  confidence: BackupConfidenceData | null,
  health: BackupHealthData | null,
): string | null {
  let latestMs = -Infinity;
  let latestIso: string | null = null;

  const consider = (candidate?: string | null) => {
    const ms = parseValidTimestamp(candidate);
    if (ms !== null && ms > latestMs && candidate) {
      latestMs = ms;
      latestIso = candidate;
    }
  };

  if (confidence) {
    for (const item of confidence.items) {
      for (const target of item.targets) {
        consider(target.lastBackupAt);
      }
      for (const evidence of item.evidence) {
        if (evidence.type === "backup" && evidence.status !== "missing") {
          consider(evidence.observedAt);
        }
      }
    }
  }

  if (health) {
    for (const staleNode of health.staleNodes) {
      consider(staleNode.lastBackupAt);
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
