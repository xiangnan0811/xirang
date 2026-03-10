export function cronToNatural(cron: string) {
  if (!cron) return "未配置";
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return `按表达式 ${cron} 执行`;
  }
  const [minute, hour, dayOfMonth, month, dayOfWeek] = parts;
  if (minute.startsWith("*/") && hour === "*" && dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    return `每隔 ${minute.replace("*/", "")} 分钟执行`;
  }
  if (hour.startsWith("*/") && dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    const hours = hour.replace("*/", "");
    return minute === "0"
      ? `每隔 ${hours} 小时整点执行`
      : `每隔 ${hours} 小时在 ${minute} 分执行`;
  }
  if (dayOfWeek !== "*") {
    const weekdayMap: Record<string, string> = {
      "0": "周日", "1": "周一", "2": "周二", "3": "周三",
      "4": "周四", "5": "周五", "6": "周六", "7": "周日"
    };
    const days = dayOfWeek.split(",").map(d => weekdayMap[d] || d).join("、");
    if (hour === "*" && minute.startsWith("*/")) {
      return `每周 ${days} 每 ${minute.replace("*/", "")} 分钟执行一次`;
    }
    return `每周 ${days} ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
  }
  if (dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    if (hour === "*" && minute === "*") return "每分钟执行";
    return `每天 ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
  }
  return `按表达式 ${cron} 执行`;
}

export function nextRunPreview(cron: string) {
  if (!cron) return "未配置定时规则";
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return "无法预估下次执行时间";
  }
  const now = new Date();
  const minute = parts[0];
  const hour = parts[1];
  const weekday = parts[4];

  if (minute.startsWith("*/") && hour === "*") {
    const interval = Number(minute.replace("*/", ""));
    if (Number.isFinite(interval) && interval > 0) {
      const next = new Date(now.getTime() + interval * 60 * 1000);
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
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
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
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
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
    }
    const targetWeekday = Number(weekday);
    if (Number.isFinite(targetWeekday)) {
      const normalizedWeekday = ((targetWeekday % 7) + 7) % 7;
      let dayOffset = normalizedWeekday - next.getDay();
      if (dayOffset < 0 || (dayOffset === 0 && next <= now)) {
        dayOffset += 7;
      }
      next.setDate(next.getDate() + dayOffset);
      const weekdayMap: Record<string, string> = {
        "0": "周日", "1": "周一", "2": "周二", "3": "周三", "4": "周四", "5": "周五", "6": "周六", "7": "周日",
      };
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}（${weekdayMap[weekday] ?? `周${weekday}`}）`;
    }
  }
  return "无法预估下次执行时间";
}
