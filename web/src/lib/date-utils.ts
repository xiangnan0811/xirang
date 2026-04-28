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
