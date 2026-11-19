import type { SSHKeyRecord } from "@/types/domain";

export function keyTypeBadgeVariant(keyType: string): "neutral" | "warning" {
  switch (keyType.toLowerCase()) {
    case "ed25519":
      return "neutral";
    case "rsa":
      return "warning";
    case "ecdsa":
      return "neutral";
    default:
      return "neutral";
  }
}

export function scopeBadges(key: SSHKeyRecord, t: (key: string) => string) {
  const badges: Array<{ label: string; tone: "neutral" | "warning" | "destructive" }> = [];
  if (key.disabled) {
    badges.push({ label: t("sshKeys.scopeDisabledBadge"), tone: "destructive" });
  }
  if (key.expiresAt) {
    badges.push({ label: t("sshKeys.scopeExpiringBadge"), tone: "warning" });
  }
  if (key.broadScope) {
    badges.push({ label: t("sshKeys.scopeBroadBadge"), tone: "warning" });
  }
  if (!badges.length) {
    badges.push({ label: t("sshKeys.scopeRestrictedBadge"), tone: "neutral" });
  }
  return badges;
}
