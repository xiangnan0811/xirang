import i18n from "@/i18n";

function getWeekdayName(d: string): string {
  return i18n.t(`cron.weekdayNames.${d}`, { defaultValue: d });
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

export type CronPreviewResult =
  | { status: "estimated"; nextAt: number }
  | { status: "empty" | "unsupported" | "no_match" };

const MINUTE_MS = 60_000;
const DAY_MS = 86_400_000;
const PREVIEW_HORIZON_YEARS = 5;

function range(min: number, max: number, step = 1): number[] {
  const values: number[] = [];
  for (let value = min; value <= max; value += step) values.push(value);
  return values;
}

// minute/hour：`*`、单个整数或从字段 0 开始对齐的 `*/N`。
function parseTimeField(token: string, max: number): number[] | null {
  if (token === ANY) return range(0, max);
  const step = parseStep(token, 1, max);
  if (step !== null) return range(0, max, step);
  const value = parseBoundedInt(token, 0, max);
  return value === null ? null : [value];
}

// DOM/month：`*`（返回 null 表示不限制）或单个整数；undefined 表示不支持。
function parseDateField(token: string, min: number, max: number): number | null | undefined {
  if (token === ANY) return null;
  const value = parseBoundedInt(token, min, max);
  return value === null ? undefined : value;
}

/**
 * 任务/策略编辑用的浏览器时钟 UTC 预估，只覆盖有限五段语法：
 * minute/hour 为 `*`、整数或步长 `*&#47;N`（从字段 0 对齐），DOM 与 month 为 `*` 或整数，DOW 为 `*` 或 0–6 列表。
 * descriptor、TZ=/CRON_TZ=、范围、名称、六段、越界一律 unsupported（不代表后端拒绝）。
 * 日/周都受限时按标准 cron 取 OR；仅搜索 now 起五年（不含上界），找不到返回 no_match。
 * 结果不是调度事实，服务端 nextRunAt 为准。
 */
export function nextRunPreview(cron: string, now: Date): CronPreviewResult {
  const trimmed = cron.trim();
  if (trimmed === "") return { status: "empty" };
  const nowMs = now.getTime();
  if (!Number.isFinite(nowMs)) return { status: "unsupported" };

  const parts = trimmed.split(/\s+/);
  if (parts.length !== 5) return { status: "unsupported" };
  const [minuteToken, hourToken, domToken, monthToken, dowToken] = parts;
  const minutes = parseTimeField(minuteToken, 59);
  const hours = parseTimeField(hourToken, 23);
  const dayOfMonth = parseDateField(domToken, 1, 31);
  const month = parseDateField(monthToken, 1, 12);
  const weekdays = dowToken === ANY ? null : parseWeekdayList(dowToken)?.map(Number);
  if (minutes === null || hours === null || dayOfMonth === undefined || month === undefined || weekdays === undefined) {
    return { status: "unsupported" };
  }
  const weekdaySet = weekdays === null ? null : new Set(weekdays);

  // 用克隆 + setUTC* 而非 Date.UTC：Date.UTC 会把 0–99 年映射到 1900–1999。
  // setUTCFullYear 保留日期归一化（2 月 29 日 + 5 年 → 3 月 1 日），上界不含。
  const horizon = new Date(nowMs);
  horizon.setUTCFullYear(horizon.getUTCFullYear() + PREVIEW_HORIZON_YEARS);
  const horizonMs = horizon.getTime();
  const dayStart = new Date(nowMs);
  dayStart.setUTCHours(0, 0, 0, 0);

  // 逐个真实存在的 UTC 日历日筛选日期，再按升序枚举日内 hour/minute。
  for (let dayMs = dayStart.getTime(); dayMs < horizonMs; dayMs += DAY_MS) {
    const day = new Date(dayMs);
    if (month !== null && day.getUTCMonth() + 1 !== month) continue;
    const domMatches = dayOfMonth === null || day.getUTCDate() === dayOfMonth;
    const dowMatches = weekdaySet === null || weekdaySet.has(day.getUTCDay());
    const dateMatches =
      dayOfMonth !== null && weekdaySet !== null ? domMatches || dowMatches : domMatches && dowMatches;
    if (!dateMatches) continue;
    for (const hour of hours) {
      for (const minute of minutes) {
        const candidate = dayMs + hour * 60 * MINUTE_MS + minute * MINUTE_MS;
        if (candidate <= nowMs) continue;
        if (candidate >= horizonMs) return { status: "no_match" };
        return { status: "estimated", nextAt: candidate };
      }
    }
  }
  return { status: "no_match" };
}
