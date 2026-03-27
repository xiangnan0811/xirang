import { useState, useEffect, useMemo, useCallback, useRef } from "react";
import { Clock, CalendarDays, Settings2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { cronToNatural, nextRunPreview } from "@/lib/cron-utils";

type CronGeneratorProps = {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  placeholder?: string;
};

type ScheduleType = "minutes" | "hours" | "daily" | "weekly" | "monthly" | "custom";

const CRON_PRESET_KEYS = [
  { key: "daily2am", value: "0 2 * * *" },
  { key: "every6h", value: "0 */6 * * *" },
  { key: "every12h", value: "0 */12 * * *" },
  { key: "sundayMorning", value: "0 3 * * 0" },
  { key: "monthly1st", value: "0 2 1 * *" },
];

export function CronGenerator({ id, value, onChange, disabled, placeholder }: CronGeneratorProps) {
  const { t } = useTranslation();
  const resolvedPlaceholder = placeholder ?? t('cron.placeholder');
  const [isBuilderOpen, setIsBuilderOpen] = useState(false);
  const [scheduleType, setScheduleType] = useState<ScheduleType>("daily");

  // Builder states
  const [minutesInterval, setMinutesInterval] = useState("30");
  const [hoursInterval, setHoursInterval] = useState("2");
  const [hoursMinute, setHoursMinute] = useState("0");
  const [time, setTime] = useState("02:00");
  const [weekdays, setWeekdays] = useState<string[]>(["0"]); // 0 = Sunday
  const [dayOfMonth, setDayOfMonth] = useState("1");

  // Track whether the builder just opened to prevent auto-generate race condition
  const prevBuilderOpen = useRef(false);
  const skipNextAutoGen = useRef(false);

  // Sync builder state with raw cron only when the builder first opens
  useEffect(() => {
    const justOpened = isBuilderOpen && !prevBuilderOpen.current;
    prevBuilderOpen.current = isBuilderOpen;

    if (!justOpened || !value) return;

    skipNextAutoGen.current = true;
    const parts = value.trim().split(/\s+/);
    if (parts.length === 5) {
      const [min, hr, dom, mon, dow] = parts;
      if (min.startsWith("*/") && hr === "*" && dom === "*" && mon === "*" && dow === "*") {
        setScheduleType("minutes");
        setMinutesInterval(min.replace("*/", ""));
      } else if (hr.startsWith("*/") && dom === "*" && mon === "*" && dow === "*") {
        setScheduleType("hours");
        setHoursInterval(hr.replace("*/", ""));
        setHoursMinute(min);
      } else if (dom === "*" && mon === "*" && dow !== "*") {
        setScheduleType("weekly");
        setWeekdays(dow.split(","));
        setTime(`${hr.padStart(2, "0")}:${min.padStart(2, "0")}`);
      } else if (dom !== "*" && mon === "*" && dow === "*") {
        setScheduleType("monthly");
        setDayOfMonth(dom);
        setTime(`${hr.padStart(2, "0")}:${min.padStart(2, "0")}`);
      } else if (dom === "*" && mon === "*" && dow === "*") {
        setScheduleType("daily");
        setTime(`${hr.padStart(2, "0")}:${min.padStart(2, "0")}`);
      } else {
        setScheduleType("custom");
      }
    }
  }, [isBuilderOpen, value]);

  const generateCron = useCallback(() => {
    let cron = "";
    switch (scheduleType) {
      case "minutes":
        cron = `*/${minutesInterval || 1} * * * *`;
        break;
      case "hours":
        cron = `${hoursMinute || 0} */${hoursInterval || 1} * * *`;
        break;
      case "daily": {
        const [hr, min] = time.split(":");
        cron = `${Number(min)} ${Number(hr)} * * *`;
        break;
      }
      case "weekly": {
        const [hr, min] = time.split(":");
        const dow = weekdays.length > 0 ? [...weekdays].sort().join(",") : "*";
        cron = `${Number(min)} ${Number(hr)} * * ${dow}`;
        break;
      }
      case "monthly": {
        const [hr, min] = time.split(":");
        cron = `${Number(min)} ${Number(hr)} ${dayOfMonth || 1} * *`;
        break;
      }
      case "custom":
        return null;
    }
    return cron;
  }, [scheduleType, minutesInterval, hoursInterval, hoursMinute, time, weekdays, dayOfMonth]);

  // Auto-generate when builder state changes (skip initial sync to prevent race)
  useEffect(() => {
    if (!isBuilderOpen || scheduleType === "custom") return;
    if (skipNextAutoGen.current) {
      skipNextAutoGen.current = false;
      return;
    }
    const cron = generateCron();
    if (cron !== null && cron !== value) {
      onChange(cron);
    }
  }, [isBuilderOpen, scheduleType, generateCron, value, onChange]);

  const toggleWeekday = (day: string) => {
    setWeekdays((prev) =>
      prev.includes(day) ? prev.filter((d) => d !== day) : [...prev, day]
    );
  };

  const naturalDescription = useMemo(() => cronToNatural(value), [value]);
  const nextRun = useMemo(() => nextRunPreview(value), [value]);

  return (
    <div className="space-y-3">
      {/* Preset buttons */}
      <div className="flex flex-wrap gap-1.5" role="group" aria-label={t('cron.presetsAriaLabel')}>
        {CRON_PRESET_KEYS.map((preset) => (
          <button
            key={preset.value}
            type="button"
            className={`rounded-md border px-2 py-1 text-xs transition-colors ${
              value === preset.value
                ? "border-primary bg-primary/10 text-primary"
                : "border-border/60 bg-muted/30 text-muted-foreground hover:bg-muted/60 hover:text-foreground"
            }`}
            onClick={() => onChange(preset.value)}
            disabled={disabled}
          >
            {t(`cron.presets.${preset.key}`)}
          </button>
        ))}
      </div>

      <div className="flex gap-2">
        <div className="relative flex-1">
          <Input
            id={id}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            disabled={disabled}
            placeholder={resolvedPlaceholder}
            className="font-mono"
          />
        </div>
        <Button
          type="button"
          variant="outline"
          onClick={() => setIsBuilderOpen(!isBuilderOpen)}
          disabled={disabled}
          aria-expanded={isBuilderOpen}
          className="shrink-0 w-[120px]"
        >
          <Settings2 className="mr-2 size-4" />
          {isBuilderOpen ? t('cron.collapseBuilder') : t('cron.toggleBuilder')}
        </Button>
      </div>

      {isBuilderOpen && (
        <div className="rounded-md border bg-card p-4 shadow-sm animate-in slide-in-from-top-2 fade-in duration-200">
          <div className="mb-4 flex items-center gap-2 border-b pb-3">
            <span className="text-sm font-medium text-muted-foreground flex items-center gap-1.5">
              <RefreshCw className="size-4" />
              {t('cron.executionFrequency')}
            </span>
            <AppSelect
              value={scheduleType}
              onChange={(e) => setScheduleType(e.target.value as ScheduleType)}
              className="w-[180px] h-8 text-sm"
              disabled={disabled}
            >
              <option value="minutes">{t('cron.byMinute')}</option>
              <option value="hours">{t('cron.byHour')}</option>
              <option value="daily">{t('cron.daily')}</option>
              <option value="weekly">{t('cron.weekly')}</option>
              <option value="monthly">{t('cron.monthly')}</option>
              <option value="custom">{t('cron.custom')}</option>
            </AppSelect>
          </div>

          <div className="min-h-[60px]">
            {scheduleType === "minutes" && (
              <div className="flex items-center gap-2 text-sm">
                <span>{t('cron.every', '每隔')}</span>
                <Input
                  type="number"
                  min="1"
                  max="59"
                  value={minutesInterval}
                  onChange={(e) => setMinutesInterval(e.target.value)}
                  className="w-20 h-8"
                  disabled={disabled}
                  aria-label={t('cron.minuteInterval')}
                />
                <span>{t('cron.minuteExec', '分钟执行一次')}</span>
              </div>
            )}

            {scheduleType === "hours" && (
              <div className="flex items-center gap-2 text-sm flex-wrap">
                <span>{t('cron.every', '每隔')}</span>
                <Input
                  type="number"
                  min="1"
                  max="23"
                  value={hoursInterval}
                  onChange={(e) => setHoursInterval(e.target.value)}
                  className="w-20 h-8"
                  disabled={disabled}
                  aria-label={t('cron.hourInterval')}
                />
                <span>{t('cron.hourAtMinute', '小时的第')}</span>
                <Input
                  type="number"
                  min="0"
                  max="59"
                  value={hoursMinute}
                  onChange={(e) => setHoursMinute(e.target.value)}
                  className="w-20 h-8"
                  disabled={disabled}
                  aria-label={t('cron.minute')}
                />
                <span>{t('cron.exec', '分钟执行')}</span>
              </div>
            )}

            {scheduleType === "daily" && (
              <div className="flex items-center gap-2 text-sm">
                <span>{t('cron.dailyAt', '每天的')}</span>
                <Input
                  type="time"
                  value={time}
                  onChange={(e) => setTime(e.target.value)}
                  className="w-32 h-8"
                  disabled={disabled}
                  aria-label={t('cron.executionTime')}
                />
                <span>{t('cron.execute', '执行')}</span>
              </div>
            )}

            {scheduleType === "weekly" && (
              <div className="space-y-3">
                <div className="flex flex-wrap gap-1.5" role="group" aria-label={t('cron.selectWeekday')}>
                  {[
                    { val: "1", label: t('cron.weekdays.1') },
                    { val: "2", label: t('cron.weekdays.2') },
                    { val: "3", label: t('cron.weekdays.3') },
                    { val: "4", label: t('cron.weekdays.4') },
                    { val: "5", label: t('cron.weekdays.5') },
                    { val: "6", label: t('cron.weekdays.6') },
                    { val: "0", label: t('cron.weekdays.0') },
                  ].map((day) => (
                    <Button
                      key={day.val}
                      type="button"
                      variant={weekdays.includes(day.val) ? "default" : "outline"}
                      size="sm"
                      className="h-8 w-10 p-0"
                      onClick={() => toggleWeekday(day.val)}
                      disabled={disabled}
                      aria-pressed={weekdays.includes(day.val)}
                      aria-label={t('cron.weekdayAriaLabel', { day: day.label })}
                    >
                      {day.label}
                    </Button>
                  ))}
                </div>
                <div className="flex items-center gap-2 text-sm mt-3">
                  <span>{t('cron.at', '的')}</span>
                  <Input
                    type="time"
                    value={time}
                    onChange={(e) => setTime(e.target.value)}
                    className="w-32 h-8"
                    disabled={disabled}
                    aria-label={t('cron.executionTime')}
                  />
                  <span>{t('cron.execute', '执行')}</span>
                </div>
              </div>
            )}

            {scheduleType === "monthly" && (
              <div className="flex items-center gap-2 text-sm flex-wrap">
                <span>{t('cron.everyMonth', '每月')}</span>
                <Input
                  type="number"
                  min="1"
                  max="31"
                  value={dayOfMonth}
                  onChange={(e) => setDayOfMonth(e.target.value)}
                  className="w-20 h-8"
                  disabled={disabled}
                  aria-label={t('cron.date')}
                />
                <span>{t('cron.dayAt', '日的')}</span>
                <Input
                  type="time"
                  value={time}
                  onChange={(e) => setTime(e.target.value)}
                  className="w-32 h-8"
                  disabled={disabled}
                  aria-label={t('cron.executionTime')}
                />
                <span>{t('cron.execute', '执行')}</span>
              </div>
            )}

            {scheduleType === "custom" && (
              <div className="text-sm text-muted-foreground flex items-center gap-2 p-2 bg-muted/50 rounded-md">
                <Clock className="size-4" />
                <span>{t('cron.customHint', '高级自定义模式下，请直接在上方输入框中编写完整的 Cron 表达式。')}</span>
              </div>
            )}
          </div>
        </div>
      )}

      {/* Preview Section */}
      {value.trim() && (
        <div className="rounded-md border bg-muted/30 p-3 text-sm">
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground mb-1">
            <CalendarDays className="size-3.5" />
            {t('cron.parseResult', '解析结果')}
          </div>
          <p className="font-medium">{naturalDescription}</p>
          <p className="mt-1 text-xs text-info">{nextRun}</p>
        </div>
      )}
    </div>
  );
}
