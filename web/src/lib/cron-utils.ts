import i18n from "@/i18n";

function getWeekdayName(d: string): string {
  return i18n.t(`cron.weekdayNames.${d}`, { defaultValue: d });
}

function getLocaleString(date: Date): string {
  const p = (n: number) => n.toString().padStart(2, "0");
  return `${date.getFullYear()}-${p(date.getMonth() + 1)}-${p(date.getDate())} ${p(date.getHours())}:${p(date.getMinutes())}:${p(date.getSeconds())}`;
}

const ANY = "*";

// 仅接受完整十进制数字 token，并限制在 [min, max] 闭区间内；
// "*/5oops"、"1-5"、"MON"、混合列表等都不视为数字。
function parseBoundedInt(token: string, min: number, max: number): number | null {
  if (!/^\d{1,2}$/.test(token)) return null;
  const value = Number(token);
  return value >= min && value <= max ? value : null;
}

function parseStep(token: string, min: number, max: number): number | null {
  if (!token.startsWith("*/")) return null;
  return parseBoundedInt(token.slice(2), min, max);
}

function parseWeekdayList(token: string): string[] | null {
  const days = token.split(",");
  if (days.some((day) => parseBoundedInt(day, 0, 6) === null)) return null;
  return days.map((day) => String(Number(day)));
}

function formatTime(hour: number, minute: number): string {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

/**
 * 将 cron 表达式转为自然语言说明。只解释能确定语义的有限模式，
 * 其余（日期/月份限制、范围、名称、列表、descriptor、时区前缀、六段、越界等）
 * 一律原样展示表达式，避免把后端可解析的表达式误读成别的调度。
 */
export function cronToNatural(cron: string | null | undefined): string {
  if (typeof cron !== "string" || cron.trim() === "") return i18n.t("cron.notConfigured");
  const trimmed = cron.trim();
  const fallback = () => i18n.t("cron.byCronExpression", { cron: trimmed });
  const parts = trimmed.split(/\s+/);
  if (parts.length !== 5) return fallback();
  const [minute, hour, dayOfMonth, month, dayOfWeek] = parts;
  if (dayOfMonth !== ANY || month !== ANY) return fallback();

  if (dayOfWeek === ANY) {
    if (minute === ANY && hour === ANY) return i18n.t("cron.everyMinute");
    if (hour === ANY) {
      const step = parseStep(minute, 1, 59);
      if (step !== null) return i18n.t("cron.everyNMinutes", { n: step });
      const fixedMinute = parseBoundedInt(minute, 0, 59);
      if (fixedMinute !== null) return i18n.t("cron.hourlyAtMinute", { minute: fixedMinute });
      return fallback();
    }
    const fixedMinute = parseBoundedInt(minute, 0, 59);
    if (fixedMinute === null) return fallback();
    const hourStep = parseStep(hour, 1, 23);
    if (hourStep !== null) {
      return fixedMinute === 0
        ? i18n.t("cron.everyNHoursOnTheHour", { n: hourStep })
        : i18n.t("cron.everyNHoursAtMinute", { n: hourStep, minute: fixedMinute });
    }
    const fixedHour = parseBoundedInt(hour, 0, 23);
    if (fixedHour === null) return fallback();
    return i18n.t("cron.dailyAtTime", { time: formatTime(fixedHour, fixedMinute) });
  }

  const weekdays = parseWeekdayList(dayOfWeek);
  if (weekdays === null) return fallback();
  const days = weekdays.map((d) => getWeekdayName(d)).join(i18n.t("cron.weekdaySeparator"));
  if (hour === ANY) {
    const step = parseStep(minute, 1, 59);
    if (step === null) return fallback();
    return i18n.t("cron.weeklyDaysEveryNMinutes", { days, n: step });
  }
  const fixedMinute = parseBoundedInt(minute, 0, 59);
  const fixedHour = parseBoundedInt(hour, 0, 23);
  if (fixedMinute === null || fixedHour === null) return fallback();
  return i18n.t("cron.weeklyDaysAtTime", { days, time: formatTime(fixedHour, fixedMinute) });
}

/** 任务调度时间基准提示 key：带 TZ=/CRON_TZ= 前缀时由表达式指定，否则后端按 UTC 调度。 */
export function cronTimezoneNoteKey(cron: string): "cron.taskTimezoneExplicit" | "cron.taskTimezoneUTC" {
  const trimmed = cron.trim();
  return trimmed.startsWith("TZ=") || trimmed.startsWith("CRON_TZ=")
    ? "cron.taskTimezoneExplicit"
    : "cron.taskTimezoneUTC";
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
