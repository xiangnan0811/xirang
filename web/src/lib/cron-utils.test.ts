import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getLanguage, setLanguage, type SupportedLanguage } from "@/i18n";
import { cronTimezoneNoteKey, cronToNatural, nextRunPreview } from "./cron-utils";

const fallback = (cron: string) => `By expression ${cron}`;

describe("cronToNatural", () => {
  let previousLanguage: SupportedLanguage;

  beforeAll(async () => {
    previousLanguage = getLanguage();
    await setLanguage("en");
  });

  afterAll(async () => {
    await setLanguage(previousLanguage);
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
    await setLanguage("zh");
    expect(cronToNatural("0 2 * * *")).toBe("每天 02:00 执行");
    expect(cronToNatural("7 * * * *")).toBe("每小时第 7 分钟");
    await setLanguage("en");
    expect(cronToNatural("0 2 * * *")).toBe("Daily at 02:00");
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

const at = (iso: string) => new Date(iso);
const estimated = (iso: string) => ({ status: "estimated", nextAt: Date.parse(iso) });

describe("nextRunPreview", () => {
  it.each([
    ["*/15 * * * *", "2026-10-15T10:07:42Z", "2026-10-15T10:15:00Z"],
    ["*/15 * * * *", "2026-10-15T10:15:00Z", "2026-10-15T10:30:00Z"],
    ["*/59 * * * *", "2026-10-15T10:59:30Z", "2026-10-15T11:00:00Z"],
    ["30 */5 * * *", "2026-10-15T22:40:00Z", "2026-10-16T00:30:00Z"],
    ["0 2 1 * *", "2026-10-15T10:00:00Z", "2026-11-01T02:00:00Z"],
    ["0 2 31 * *", "2026-04-30T10:00:00Z", "2026-05-31T02:00:00Z"],
    ["0 2 29 2 *", "2027-03-01T00:00:00Z", "2028-02-29T02:00:00Z"],
    // DOM and DOW both restricted: standard cron OR, so the next Monday wins.
    ["0 2 1 * 1", "2026-10-02T00:00:00Z", "2026-10-05T02:00:00Z"],
  ])("estimates %s after %s as %s (UTC)", (cron, now, expected) => {
    expect(nextRunPreview(cron, at(now))).toEqual(estimated(expected));
  });

  it("returns no_match when nothing matches within five years", () => {
    expect(nextRunPreview("0 2 30 2 *", at("2026-10-15T00:00:00Z"))).toEqual({ status: "no_match" });
    expect(nextRunPreview("0 0 31 4 *", at("2026-10-15T00:00:00Z"))).toEqual({ status: "no_match" });
  });

  it("bounds the search to five years after now, exclusive", () => {
    // 2100 is not a leap year, so Feb 29 skips from 2096 to 2104 (eight years).
    expect(nextRunPreview("0 2 29 2 *", at("2096-02-29T02:00:00Z"))).toEqual({ status: "no_match" });
    // Horizon 2104-02-28T02:00Z stops just before the 2104 occurrence.
    expect(nextRunPreview("0 2 29 2 *", at("2099-02-28T02:00:00Z"))).toEqual({ status: "no_match" });
    // Horizon 2104-03-01T02:00Z includes the 2104-02-29 occurrence.
    expect(nextRunPreview("0 2 29 2 *", at("2099-03-01T02:00:00Z"))).toEqual(estimated("2104-02-29T02:00:00Z"));
    expect(nextRunPreview("0 2 29 2 *", at("2023-02-28T02:00:00Z"))).toEqual(estimated("2024-02-29T02:00:00Z"));
    expect(nextRunPreview("0 2 1 3 *", at("2026-03-01T02:00:00Z"))).toEqual(estimated("2027-03-01T02:00:00Z"));
  });

  it("keeps years 0–99 intact instead of remapping them to 1900–1999", () => {
    expect(nextRunPreview("*/15 * * * *", at("0050-10-15T10:07:42Z"))).toEqual(estimated("0050-10-15T10:15:00Z"));
    expect(nextRunPreview("0 0 * * *", at("0000-06-01T12:00:00Z"))).toEqual(estimated("0000-06-02T00:00:00Z"));
    expect(nextRunPreview("0 0 1 1 *", at("0099-12-31T12:00:00Z"))).toEqual(estimated("0100-01-01T00:00:00Z"));
    // Year 100 is not a leap year: Feb 29 skips 0096 -> 0104, beyond the 0101-03-01 horizon.
    expect(nextRunPreview("0 2 29 2 *", at("0096-02-29T02:00:00Z"))).toEqual({ status: "no_match" });
    // Horizon 0104-03-01T02:00Z includes the 0104-02-29 occurrence.
    expect(nextRunPreview("0 2 29 2 *", at("0099-03-01T02:00:00Z"))).toEqual(estimated("0104-02-29T02:00:00Z"));
  });

  it("does not treat an explicit timezone prefix as UTC", () => {
    for (const cron of ["TZ=America/New_York 30 2 * * *", "CRON_TZ=Asia/Shanghai 0 2 * * *"]) {
      expect(nextRunPreview(cron, at("2026-10-15T00:00:00Z"))).toEqual({ status: "unsupported" });
    }
  });

  it("is strictly after now and ignores sub-minute precision", () => {
    expect(nextRunPreview("* * * * *", at("2026-10-15T10:07:00Z"))).toEqual(estimated("2026-10-15T10:08:00Z"));
    expect(nextRunPreview("* * * * *", at("2026-10-15T10:07:59.999Z"))).toEqual(estimated("2026-10-15T10:08:00Z"));
    expect(nextRunPreview("0 0 * * *", at("2026-12-31T23:59:30Z"))).toEqual(estimated("2027-01-01T00:00:00Z"));
  });

  it("aligns steps to the field zero across hour and day boundaries", () => {
    expect(nextRunPreview("*/7 * * * *", at("2026-10-15T10:57:00Z"))).toEqual(estimated("2026-10-15T11:00:00Z"));
    expect(nextRunPreview("*/25 * * * *", at("2026-10-15T23:51:00Z"))).toEqual(estimated("2026-10-16T00:00:00Z"));
    expect(nextRunPreview("0 */7 * * *", at("2026-10-15T21:00:00Z"))).toEqual(estimated("2026-10-16T00:00:00Z"));
    expect(nextRunPreview("*/10 */6 * * *", at("2026-10-15T00:55:00Z"))).toEqual(estimated("2026-10-15T06:00:00Z"));
  });

  it("matches any of several weekdays", () => {
    // 2026-10-15 is a Thursday.
    expect(nextRunPreview("15 8 * * 1,3,5", at("2026-10-15T09:00:00Z"))).toEqual(estimated("2026-10-16T08:15:00Z"));
    expect(nextRunPreview("15 8 * * 1,3,5", at("2026-10-16T09:00:00Z"))).toEqual(estimated("2026-10-19T08:15:00Z"));
    expect(nextRunPreview("0 3 * * 6,0", at("2026-10-15T09:00:00Z"))).toEqual(estimated("2026-10-17T03:00:00Z"));
    expect(nextRunPreview("0 3 * * 0", at("2026-10-15T09:00:00Z"))).toEqual(estimated("2026-10-18T03:00:00Z"));
  });

  it("combines month restrictions with steps and day filters", () => {
    expect(nextRunPreview("*/30 * * 12 *", at("2026-10-15T10:00:00Z"))).toEqual(estimated("2026-12-01T00:00:00Z"));
    expect(nextRunPreview("*/30 * * 10 *", at("2026-10-15T10:10:00Z"))).toEqual(estimated("2026-10-15T10:30:00Z"));
    expect(nextRunPreview("0 */12 * 1 1", at("2026-10-15T10:00:00Z"))).toEqual(estimated("2027-01-04T00:00:00Z"));
    expect(nextRunPreview("0 9 15 * *", at("2026-10-15T09:00:00Z"))).toEqual(estimated("2026-11-15T09:00:00Z"));
    // DOM restricted with DOW *, and DOW restricted with DOM *: only the restricted field applies.
    expect(nextRunPreview("0 9 20 * *", at("2026-10-15T00:00:00Z"))).toEqual(estimated("2026-10-20T09:00:00Z"));
    expect(nextRunPreview("0 9 * 11 2", at("2026-10-15T00:00:00Z"))).toEqual(estimated("2026-11-03T09:00:00Z"));
  });

  it("never normalizes a missing calendar day into a match", () => {
    expect(nextRunPreview("0 0 31 * *", at("2026-09-01T00:00:00Z"))).toEqual(estimated("2026-10-31T00:00:00Z"));
    expect(nextRunPreview("0 0 31 6 *", at("2026-05-31T00:00:00Z"))).toEqual({ status: "no_match" });
    expect(nextRunPreview("0 0 29 2 *", at("2026-02-01T00:00:00Z"))).toEqual(estimated("2028-02-29T00:00:00Z"));
  });

  it.each([
    ["minutes", "*/5 * * * *", "2026-10-15T10:07:42Z", "2026-10-15T10:10:00Z"],
    ["minutes default", "*/1 * * * *", "2026-10-15T10:07:42Z", "2026-10-15T10:08:00Z"],
    ["hours", "15 */2 * * *", "2026-10-15T10:20:00Z", "2026-10-15T12:15:00Z"],
    ["daily", "30 2 * * *", "2026-10-15T10:00:00Z", "2026-10-16T02:30:00Z"],
    ["weekly", "0 9 * * 1,5", "2026-10-15T10:00:00Z", "2026-10-16T09:00:00Z"],
    ["weekly without days", "0 9 * * *", "2026-10-15T10:00:00Z", "2026-10-16T09:00:00Z"],
    ["monthly", "0 2 31 * *", "2026-10-15T10:00:00Z", "2026-10-31T02:00:00Z"],
    ["custom with padded zeros", "05 09 * * *", "2026-10-15T10:00:00Z", "2026-10-16T09:05:00Z"],
    ["custom with extra whitespace", "  0   2  * *  * ", "2026-10-15T10:00:00Z", "2026-10-16T02:00:00Z"],
  ])("estimates generator mode %s (%s)", (_mode, cron, now, expected) => {
    expect(nextRunPreview(cron, at(now))).toEqual(estimated(expected));
  });

  it.each(["", "   ", "\t\n"])("returns empty for blank input %j", (cron) => {
    expect(nextRunPreview(cron, at("2026-10-15T10:00:00Z"))).toEqual({ status: "empty" });
  });

  it("returns unsupported for an invalid Date", () => {
    expect(nextRunPreview("* * * * *", new Date(Number.NaN))).toEqual({ status: "unsupported" });
  });

  it.each([
    // Field counts and descriptors.
    "0 2 * *",
    "0 0 2 * * *",
    "@daily",
    "@every 1h",
    // Zero steps, out-of-range values and step bounds.
    "*/0 * * * *",
    "*/60 * * * *",
    "0 */0 * * *",
    "0 */24 * * *",
    "60 * * * *",
    "0 24 * * *",
    "0 2 0 * *",
    "0 2 32 * *",
    "0 2 * 0 *",
    "0 2 * 13 *",
    "0 2 * * 7",
    "000 2 * * *",
    "-1 2 * * *",
    // Trailing garbage, names, ranges, lists outside DOW, steps outside minute/hour.
    "*/5oops * * * *",
    "5x * * * *",
    "0 2 * * MON",
    "0 2 * JAN *",
    "0 2 * * 1-5",
    "0-10 * * * *",
    "0,30 * * * *",
    "0 1,13 * * *",
    "0 2 1,15 * *",
    "0 2 */2 * *",
    "0 2 * */3 *",
    "0 2 * * */2",
    "0 2 ? * *",
    "0 2 L * *",
    // Empty weekday list members.
    "0 2 * * 1,",
    "0 2 * * ,1",
    "0 2 * * 1,,3",
  ])("returns unsupported for %s", (cron) => {
    expect(nextRunPreview(cron, at("2026-10-15T10:00:00Z"))).toEqual({ status: "unsupported" });
  });

  it("yields the next UTC minute around America/New_York DST transitions", () => {
    // Spring forward: 2026-03-08 02:00 EST -> 03:00 EDT (07:00Z).
    expect(nextRunPreview("* * * * *", at("2026-03-08T06:59:30Z"))).toEqual(estimated("2026-03-08T07:00:00Z"));
    expect(nextRunPreview("* * * * *", at("2026-03-08T07:00:00Z"))).toEqual(estimated("2026-03-08T07:01:00Z"));
    // Fall back: 2026-11-01 02:00 EDT -> 01:00 EST (06:00Z).
    expect(nextRunPreview("* * * * *", at("2026-11-01T05:59:59Z"))).toEqual(estimated("2026-11-01T06:00:00Z"));
    expect(nextRunPreview("* * * * *", at("2026-11-01T06:00:00Z"))).toEqual(estimated("2026-11-01T06:01:00Z"));
    // A UTC-fixed daily time stays at the same instant regardless of local offset changes.
    expect(nextRunPreview("30 6 * * *", at("2026-03-08T00:00:00Z"))).toEqual(estimated("2026-03-08T06:30:00Z"));
    expect(nextRunPreview("30 6 * * *", at("2026-11-01T00:00:00Z"))).toEqual(estimated("2026-11-01T06:30:00Z"));
  });

  it("returns the same instant for equivalent Date objects regardless of construction", () => {
    const instant = Date.parse("2026-10-15T10:07:42Z");
    const fromLocalParts = new Date(instant);
    const viaLocal = new Date(
      fromLocalParts.getFullYear(),
      fromLocalParts.getMonth(),
      fromLocalParts.getDate(),
      fromLocalParts.getHours(),
      fromLocalParts.getMinutes(),
      fromLocalParts.getSeconds(),
    );
    expect(nextRunPreview("0 */5 * * *", viaLocal)).toEqual(nextRunPreview("0 */5 * * *", new Date(instant)));
    expect(nextRunPreview("0 */5 * * *", viaLocal)).toEqual(estimated("2026-10-15T15:00:00Z"));
  });
});
