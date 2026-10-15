import type { OverviewStats, OverviewSummary, PolicyRecord } from "@/types/domain";

export function deriveOverview(
  policies: PolicyRecord[],
  summary?: OverviewSummary | null
): OverviewStats {
  return {
    activePolicies: summary?.activePolicies ?? policies.filter((policy) => policy.enabled).length,
  };
}

export function parseTags(raw: string) {
  return raw
    .split(",")
    .map((tag) => tag.trim())
    .filter(Boolean);
}

export function buildFingerprint(privateKey: string) {
  const raw = privateKey.trim();
  let checksum = 0;
  for (let idx = 0; idx < raw.length; idx += 1) {
    checksum = (checksum + raw.charCodeAt(idx) * (idx + 3)) % 1_000_000;
  }
  return `DEMO:${checksum.toString(16).padStart(6, "0")}`;
}

export function createKeyId(name: string) {
  return `key-${name.toLowerCase().replace(/\s+/g, "-")}-${Date.now().toString(36)}`;
}

export function createIntegrationId(name: string) {
  return `int-${name.toLowerCase().replace(/\s+/g, "-")}-${Date.now().toString(36)}`;
}
