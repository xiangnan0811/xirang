function pad(n: number): string {
  return n.toString().padStart(2, "0");
}

// 统一时间显示格式 YYYY-MM-DD HH:mm:ss，语言无关（中英文一致），本地时区。
export function formatTime(input?: string | null): string {
  if (!input) return "-";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return input;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function formatDateOnly(input?: string | null): string {
  if (!input) return "-";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return input;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function formatTimeOnly(input?: string | null): string {
  if (!input) return "-";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return input;
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

const DATETIME_LOCAL_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/;

// 把 RFC3339 时间转为 `<input type="datetime-local">` 的本地分钟值；缺失/非法 → ""。
export function toDatetimeLocalValue(input?: string | null): string {
  if (!input) return "";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// 严格解析 datetime-local 值为本地时间。格式不符、日历越界或因夏令时跳变而
// 不存在的本地时刻返回 null；重复时刻沿用 Date 的既有选择（较早的偏移）。
export function parseDatetimeLocalValue(value: string): Date | null {
  const match = DATETIME_LOCAL_PATTERN.exec(value.trim());
  if (!match) return null;
  const [year, month, day, hour, minute] = match.slice(1, 6).map(Number);
  const second = match[6] ? Number(match[6]) : 0;
  const parsed = new Date(year, month - 1, day, hour, minute, second, 0);
  if (
    Number.isNaN(parsed.getTime()) ||
    parsed.getFullYear() !== year ||
    parsed.getMonth() !== month - 1 ||
    parsed.getDate() !== day ||
    parsed.getHours() !== hour ||
    parsed.getMinutes() !== minute ||
    parsed.getSeconds() !== second
  ) {
    return null;
  }
  return parsed;
}

// 本地时区相对 UTC 的偏移，例如 "UTC+08:00"、"UTC-04:30"。
export function formatUtcOffset(date: Date = new Date()): string {
  const totalMinutes = -date.getTimezoneOffset();
  const sign = totalMinutes >= 0 ? "+" : "-";
  const absolute = Math.abs(totalMinutes);
  return `UTC${sign}${pad(Math.floor(absolute / 60))}:${pad(absolute % 60)}`;
}

// 任务运行时长的规范显示（无效/缺失 → "—"，合法 0ms 照常显示）。
export function formatTaskDuration(durationMs?: number): string {
  if (typeof durationMs !== "number" || !Number.isFinite(durationMs) || durationMs < 0) {
    return "—";
  }
  if (durationMs < 1000) {
    return `${Math.round(durationMs)}ms`;
  }
  const totalSeconds = Math.floor(durationMs / 1000);
  if (totalSeconds < 60) {
    return `${totalSeconds}s`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const remainSeconds = totalSeconds % 60;
  if (minutes < 60) {
    return `${minutes}m ${remainSeconds}s`;
  }
  const hours = Math.floor(minutes / 60);
  const remainMinutes = minutes % 60;
  return `${hours}h ${remainMinutes}m`;
}

export type TimeOfDay = "morning" | "afternoon" | "evening" | "night";

export function getTimeOfDay(now: Date = new Date()): TimeOfDay {
  const h = now.getHours();
  if (h >= 5 && h < 12) return "morning";
  if (h >= 12 && h < 18) return "afternoon";
  if (h >= 18 && h < 23) return "evening";
  return "night";
}

type Locale = "en" | "zh";

const PHRASES: Record<Locale, {
  justNow: string;
  minutes: (n: number) => string;
  hours: (n: number) => string;
  days: (n: number) => string;
}> = {
  en: {
    justNow: "just now",
    minutes: (n) => `${n} minute${n === 1 ? "" : "s"} ago`,
    hours: (n) => `${n} hour${n === 1 ? "" : "s"} ago`,
    days: (n) => `${n} day${n === 1 ? "" : "s"} ago`,
  },
  zh: {
    justNow: "刚刚",
    minutes: (n) => `${n} 分钟前`,
    hours: (n) => `${n} 小时前`,
    days: (n) => `${n} 天前`,
  },
};

export function formatRelativeTime(past: Date | string, locale: Locale = "en", now: Date = new Date()): string {
  const then = past instanceof Date ? past : new Date(past);
  const diffSec = Math.floor((now.getTime() - then.getTime()) / 1000);
  const p = PHRASES[locale];
  if (diffSec < 60) return p.justNow;
  if (diffSec < 3600) return p.minutes(Math.floor(diffSec / 60));
  if (diffSec < 86400) return p.hours(Math.floor(diffSec / 3600));
  return p.days(Math.floor(diffSec / 86400));
}
