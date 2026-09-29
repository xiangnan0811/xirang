import i18n from "@/i18n";
import type { OverviewStats, OverviewSummary, PolicyRecord } from "@/types/domain";

export function deriveOverview(
  policies: PolicyRecord[],
  summary?: OverviewSummary | null
): OverviewStats {
  return {
    activePolicies: summary?.activePolicies ?? policies.filter((policy) => policy.enabled).length,
  };
}

export function describeCron(cron: string | null | undefined) {
  // 防御：API 偶发返回非标准信封时 mapPolicy 可能将 cron 设为 undefined，
  // 这里返回安全占位避免顶级渲染崩溃（详见 policy_handler Update 警告分支修复）。
  if (typeof cron !== "string" || cron.trim() === "") {
    return i18n.t("cron.byCronExpression", { cron: cron ?? "" });
  }
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return i18n.t("cron.byCronExpression", { cron });
  }
  const [minute, hour, , , weekday] = parts;
  if (minute.startsWith("*/")) {
    return i18n.t("cron.everyNMinutes", { n: minute.replace("*/", "") });
  }
  if (hour.startsWith("*/")) {
    const interval = hour.replace("*/", "");
    return minute === "0"
      ? i18n.t("cron.everyNHoursOnTheHour", { n: interval })
      : i18n.t("cron.everyNHoursAtMinute", { n: interval, minute });
  }
  if (weekday !== "*") {
    return i18n.t("cron.weeklyDaysAtTime", {
      days: weekday,
      time: `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`
    });
  }
  return i18n.t("cron.dailyAtTime", { time: `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}` });
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
