import { useTranslation } from "react-i18next";
import { useTheme } from "@/context/theme-context";
import { useRefreshInterval, useDefaultPageSize, useDatetimeFormat } from "@/hooks/use-user-preferences";

export function PersonalTab() {
  const { t, i18n } = useTranslation();
  const { theme, setTheme, density, setDensity, powerMode, setPowerMode } = useTheme();
  const [refreshInterval, setRefreshInterval] = useRefreshInterval();
  const [defaultPageSize, setDefaultPageSize] = useDefaultPageSize();
  const [datetimeFormat, setDatetimeFormat] = useDatetimeFormat();

  const selectClass = "h-9 rounded-md border border-input bg-background px-3 text-sm";

  return (
    <div className="space-y-6 max-w-2xl">
      <h2 className="text-lg font-semibold">{t("settings.personal.title")}</h2>

      <div className="glass-panel relative overflow-hidden p-5 space-y-6">
        <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />

        {/* 主题 */}
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.personal.theme")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.personal.themeDesc")}</p>
          </div>
          <select className={selectClass} value={theme} onChange={(e) => setTheme(e.target.value as "light" | "dark")}>
            <option value="light">{t("settings.personal.themeLight")}</option>
            <option value="dark">{t("settings.personal.themeDark")}</option>
            <option value="system">{t("settings.personal.themeSystem")}</option>
          </select>
        </div>

        {/* 显示密度 */}
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.personal.density")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.personal.densityDesc")}</p>
          </div>
          <select className={selectClass} value={density} onChange={(e) => setDensity(e.target.value as "comfortable" | "compact")}>
            <option value="comfortable">{t("settings.personal.densityComfortable")}</option>
            <option value="compact">{t("settings.personal.densityCompact")}</option>
          </select>
        </div>

        {/* 省电模式 */}
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.personal.powerMode")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.personal.powerModeDesc")}</p>
          </div>
          <select className={selectClass} value={powerMode} onChange={(e) => setPowerMode(e.target.value as "normal" | "save")}>
            <option value="normal">{t("settings.personal.powerNormal")}</option>
            <option value="save">{t("settings.personal.powerSave")}</option>
          </select>
        </div>

        {/* 语言 */}
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.personal.language")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.personal.languageDesc")}</p>
          </div>
          <select className={selectClass} value={i18n.language} onChange={(e) => i18n.changeLanguage(e.target.value)}>
            <option value="zh">中文</option>
            <option value="en">English</option>
          </select>
        </div>

        {/* 自动刷新间隔 */}
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.personal.refreshInterval")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.personal.refreshIntervalDesc")}</p>
          </div>
          <select className={selectClass} value={String(refreshInterval)} onChange={(e) => setRefreshInterval(e.target.value)}>
            <option value="0">{t("settings.personal.refreshOff")}</option>
            <option value="30">30s</option>
            <option value="60">60s</option>
            <option value="120">120s</option>
            <option value="300">300s</option>
          </select>
        </div>

        {/* 默认分页条数 */}
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.personal.defaultPageSize")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.personal.defaultPageSizeDesc")}</p>
          </div>
          <select className={selectClass} value={String(defaultPageSize)} onChange={(e) => setDefaultPageSize(e.target.value)}>
            <option value="10">10</option>
            <option value="25">25</option>
            <option value="50">50</option>
            <option value="100">100</option>
          </select>
        </div>

        {/* 日期格式 */}
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.personal.datetimeFormat")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.personal.datetimeFormatDesc")}</p>
          </div>
          <select className={selectClass} value={datetimeFormat} onChange={(e) => setDatetimeFormat(e.target.value)}>
            <option value="locale">{t("settings.personal.dtLocale")}</option>
            <option value="iso">ISO 8601</option>
            <option value="relative">{t("settings.personal.dtRelative")}</option>
          </select>
        </div>
      </div>
    </div>
  );
}
