import i18n from "@/i18n";

function getWeekdayName(d: string): string {
  return i18n.t(`cron.weekdayNames.${d}`, { defaultValue: d });
}

function getLocaleString(date: Date): string {
  const p = (n: number) => n.toString().padStart(2, "0");
  return `${date.getFullYear()}-${p(date.getMonth() + 1)}-${p(date.getDate())} ${p(date.getHours())}:${p(date.getMinutes())}:${p(date.getSeconds())}`;
}

export function cronToNatural(cron: string) {
  if (!cron) return i18n.t("cron.notConfigured");
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return i18n.t("cron.byCronExpression", { cron });
  }
  const [minute, hour, dayOfMonth, month, dayOfWeek] = parts;
  if (minute.startsWith("*/") && hour === "*" && dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    return i18n.t("cron.everyNMinutes", { n: minute.replace("*/", "") });
  }
  if (hour.startsWith("*/") && dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    const hours = hour.replace("*/", "");
    return minute === "0"
      ? i18n.t("cron.everyNHoursOnTheHour", { n: hours })
      : i18n.t("cron.everyNHoursAtMinute", { n: hours, minute });
  }
  if (dayOfWeek !== "*") {
    const days = dayOfWeek.split(",").map(d => getWeekdayName(d)).join(i18n.t("cron.weekdaySeparator"));
    if (hour === "*" && minute.startsWith("*/")) {
      return i18n.t("cron.weeklyDaysEveryNMinutes", { days, n: minute.replace("*/", "") });
    }
    return i18n.t("cron.weeklyDaysAtTime", { days, time: `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}` });
  }
  if (dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    if (hour === "*" && minute === "*") return i18n.t("cron.everyMinute");
    return i18n.t("cron.dailyAtTime", { time: `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}` });
  }
  return i18n.t("cron.byCronExpression", { cron });
}

export function nextRunPreview(cron: string) {
  if (!cron) return i18n.t("cron.notConfiguredCron");
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return i18n.t("cron.cannotEstimate");
  }
  const now = new Date();
  const minute = parts[0];
  const hour = parts[1];
  const weekday = parts[4];

  if (minute.startsWith("*/") && hour === "*") {
    const interval = Number(minute.replace("*/", ""));
    if (Number.isFinite(interval) && interval > 0) {
      const next = new Date(now.getTime() + interval * 60 * 1000);
      return i18n.t("cron.estimatedNext", { time: getLocaleString(next) });
    }
  }
  if (hour.startsWith("*/")) {
    const interval = Number(hour.replace("*/", ""));
    const minuteValue = Number(minute);
    if (
      Number.isFinite(interval) &&
      interval > 0 &&
      Number.isFinite(minuteValue)
    ) {
      const next = new Date(now);
      next.setMinutes(minuteValue, 0, 0);
      while (next <= now) {
        next.setHours(next.getHours() + interval);
      }
      return i18n.t("cron.estimatedNext", { time: getLocaleString(next) });
    }
  }
  const minuteValue = Number(minute);
  const hourValue = Number(hour);
  if (Number.isFinite(minuteValue) && Number.isFinite(hourValue)) {
    const next = new Date(now);
    next.setHours(hourValue, minuteValue, 0, 0);
    if (weekday === "*") {
      if (next <= now) {
        next.setDate(next.getDate() + 1);
      }
      return i18n.t("cron.estimatedNext", { time: getLocaleString(next) });
    }
    const targetWeekday = Number(weekday);
    if (Number.isFinite(targetWeekday)) {
      const normalizedWeekday = ((targetWeekday % 7) + 7) % 7;
      let dayOffset = normalizedWeekday - next.getDay();
      if (dayOffset < 0 || (dayOffset === 0 && next <= now)) {
        dayOffset += 7;
      }
      next.setDate(next.getDate() + dayOffset);
      const weekdayName = getWeekdayName(weekday);
      return i18n.t("cron.estimatedNextWithWeekday", { time: getLocaleString(next), weekday: weekdayName });
    }
  }
  return i18n.t("cron.cannotEstimate");
}
