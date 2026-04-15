import { useTranslation } from "react-i18next";
import { RefreshCw } from "lucide-react";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { FilterPanel } from "@/components/ui/filter-panel";
import { SearchInput } from "@/components/ui/search-input";
import { ViewModeToggle, type ViewMode } from "@/components/ui/view-mode-toggle";

export type AlertFiltersProps = {
  keyword: string;
  onKeywordChange: (value: string) => void;
  severityFilter: string;
  onSeverityChange: (value: string) => void;
  statusFilter: string;
  onStatusChange: (value: string) => void;
  viewMode: ViewMode;
  onViewModeChange: (value: ViewMode) => void;
  total: number;
  onReset: () => void;
};

export function AlertFilters({
  keyword,
  onKeywordChange,
  severityFilter,
  onSeverityChange,
  statusFilter,
  onStatusChange,
  viewMode,
  onViewModeChange,
  total,
  onReset,
}: AlertFiltersProps) {
  const { t } = useTranslation();

  return (
    <>
      {/* 标题栏 */}
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-2 font-medium">
          {t("notifications.alertCenterTitle")}
        </div>
        <div className="flex items-center gap-2">
          <ViewModeToggle
            value={viewMode}
            onChange={onViewModeChange}
            groupLabel={t("notifications.viewModeLabel")}
            className="hidden md:inline-flex"
          />
          <Button size="sm" variant="outline" onClick={onReset}>
            <RefreshCw className="mr-1 size-3.5" />
            {t("common.resetFilter")}
          </Button>
        </div>
      </div>

      {/* 筛选栏 */}
      <FilterPanel sticky={false} className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2fr_1fr_1fr_auto] items-center">
        <SearchInput
          containerClassName="w-full"
          aria-label={t("notifications.keywordFilter")}
          value={keyword}
          onChange={(event) => onKeywordChange(event.target.value)}
          placeholder={t("notifications.keywordPlaceholder")}
        />
        <AppSelect
          containerClassName="w-full"
          aria-label={t("notifications.severityFilter")}
          value={severityFilter}
          onChange={(event) => onSeverityChange(event.target.value)}
        >
          <option value="all">{t("notifications.allSeverities")}</option>
          <option value="critical">{t("status.alert.critical")}</option>
          <option value="warning">{t("status.alert.warning")}</option>
          <option value="info">{t("status.alert.info")}</option>
        </AppSelect>
        <AppSelect
          containerClassName="w-full"
          aria-label={t("notifications.statusFilter")}
          value={statusFilter}
          onChange={(event) => onStatusChange(event.target.value)}
        >
          <option value="all">{t("common.all")}{t("common.status")}</option>
          <option value="unresolved">{t("notifications.statusUnresolved")}</option>
          <option value="open">{t("notifications.statusOpen")}</option>
          <option value="acked">{t("notifications.statusAcked")}</option>
          <option value="resolved">{t("notifications.statusResolved")}</option>
        </AppSelect>
      </FilterPanel>

      {/* 筛选摘要 */}
      <p className="text-xs text-muted-foreground">{t("common.totalItems", { total })}</p>
    </>
  );
}
