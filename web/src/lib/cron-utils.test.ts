import { afterAll, beforeAll, describe, expect, it } from "vitest";
import i18n from "@/i18n";
import { cronTimezoneNoteKey, cronToNatural } from "./cron-utils";

const fallback = (cron: string) => `By expression ${cron}`;

describe("cronToNatural", () => {
  let previousLanguage: string;

  beforeAll(async () => {
    previousLanguage = i18n.language;
    await i18n.changeLanguage("en");
  });

  afterAll(async () => {
    await i18n.changeLanguage(previousLanguage);
  });

  it.each([
    ["* * * * *", "Every minute"],
    ["*/15 * * * *", "Every 15 minutes"],
    ["*/1 * * * *", "Every 1 minutes"],
    ["*/59 * * * *", "Every 59 minutes"],
    ["7 * * * *", "Every hour at minute 7"],
    ["0 * * * *", "Every hour at minute 0"],
    ["0 */6 * * *", "Every 6 hours on the hour"],
    ["30 */2 * * *", "Every 2 hours at minute 30"],
    ["0 2 * * *", "Daily at 02:00"],
    ["59 23 * * *", "Daily at 23:59"],
    ["  0 2 * * *  ", "Daily at 02:00"],
    ["0 9 * * 1", "Mon at 09:00"],
    ["15 8 * * 1,3,5", "Mon, Wed, Fri at 08:15"],
    ["0 3 * * 0", "Sun at 03:00"],
    ["*/10 * * * 6,0", "Sat, Sun every 10 minutes"],
  ])("explains deterministic pattern %s", (cron, expected) => {
    expect(cronToNatural(cron)).toBe(expected);
  });

  it.each([
    // Day-of-month / month restrictions must never be read as daily or weekly.
    "0 2 1 * *",
    "0 2 * 1 *",
    "0 2 * 1 1",
    "0 2 1 * 1",
    // Ranges, names, steps and lists outside the supported subset.
    "0 2 * * 1-5",
    "0 2 * * MON",
    "0 2 * * 7",
    "0 2 * * 1,",
    "0,30 * * * *",
    "0 1,13 * * *",
    "0-10 * * * *",
    "*/5oops * * * *",
    "*/0 * * * *",
    "*/60 * * * *",
    "0 */24 * * *",
    "0 */0 * * *",
    "*/5 */2 * * *",
    "0 * * * 1",
    "5 */2 * * 1",
    // Out-of-range numbers.
    "60 * * * *",
    "0 24 * * *",
    "000 2 * * *",
    // Descriptors, timezone prefixes and field-count mismatches.
    "@daily",
    "@every 1h",
    "CRON_TZ=Asia/Shanghai 0 2 * * *",
    "TZ=UTC 0 2 * * *",
    "0 0 2 * * *",
    "0 2 * *",
  ])("shows the raw expression for undetermined pattern %s", (cron) => {
    expect(cronToNatural(cron)).toBe(fallback(cron));
  });

  it.each([undefined, null, "", "   "])("returns the not-configured label for %j", (cron) => {
    expect(cronToNatural(cron)).toBe("Not configured");
  });

  it("follows the active language for the same expression", async () => {
    expect(cronToNatural("0 2 * * *")).toBe("Daily at 02:00");
    await i18n.changeLanguage("zh");
    expect(cronToNatural("0 2 * * *")).toBe("每天 02:00 执行");
    expect(cronToNatural("7 * * * *")).toBe("每小时第 7 分钟");
    await i18n.changeLanguage("en");
  });
});

describe("cronTimezoneNoteKey", () => {
  it.each([
    ["0 2 * * *", "cron.taskTimezoneUTC"],
    ["@daily", "cron.taskTimezoneUTC"],
    ["CRON_TZ=Asia/Shanghai 0 2 * * *", "cron.taskTimezoneExplicit"],
    ["  TZ=UTC 0 2 * * *", "cron.taskTimezoneExplicit"],
  ])("selects the timezone note for %s", (cron, expected) => {
    expect(cronTimezoneNoteKey(cron)).toBe(expected);
  });
});
