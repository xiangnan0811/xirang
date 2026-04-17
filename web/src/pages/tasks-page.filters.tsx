import { useTranslation } from "react-i18next";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { SearchInput } from "@/components/ui/search-input";
import type { NodeRecord, TaskStatus } from "@/types/domain";

export type TasksFiltersProps = {
  keyword: string;
  setKeyword: (value: string) => void;
  statusFilter: "all" | "paused" | TaskStatus;
  setStatusFilterRaw: (value: string) => void;
  nodeFilter: string;
  setNodeFilter: (value: string) => void;
  nodes: NodeRecord[];
  filteredCount: number;
  totalCount: number;
  resetFilters: () => void;
};

export function TasksFilters({
  keyword,
  setKeyword,
  statusFilter,
  setStatusFilterRaw,
  nodeFilter,
  setNodeFilter,
  nodes,
  filteredCount,
  totalCount,
  resetFilters,
}: TasksFiltersProps) {
  const { t } = useTranslation();

  return (
    <>
      <FilterPanel sticky={false} className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2fr_1fr_1fr_auto] items-center">
        <SearchInput
          containerClassName="w-full"
          placeholder={t("tasks.searchPlaceholder")}
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
          aria-label={t("tasks.searchAriaLabel")}
        />

        <Select
          containerClassName="w-full"
          aria-label={t("tasks.statusFilterAriaLabel")}
          value={statusFilter}
          onChange={(event) =>
            setStatusFilterRaw(event.target.value)
          }
        >
          <option value="all">{t("tasks.allStatus")}</option>
          <option value="pending">{t("tasks.statusPending")}</option>
          <option value="running">{t("tasks.statusRunning")}</option>
          <option value="retrying">{t("tasks.statusRetrying")}</option>
          <option value="failed">{t("tasks.statusFailed")}</option>
          <option value="success">{t("tasks.statusSuccess")}</option>
          <option value="canceled">{t("tasks.statusCanceled")}</option>
          <option value="warning">{t("tasks.statusWarning")}</option>
          <option value="paused">{t("tasks.statusPaused")}</option>
        </Select>

        <Select
          containerClassName="w-full"
          aria-label={t("tasks.nodeFilterAriaLabel")}
          value={nodeFilter}
          onChange={(event) => setNodeFilter(event.target.value)}
        >
          <option value="all">{t("tasks.allNodes")}</option>
          {nodes.map((node) => (
            <option key={node.id} value={String(node.id)}>
              {node.name}
            </option>
          ))}
        </Select>

        <div className="flex items-center gap-2 justify-end col-span-full sm:col-span-2 md:col-span-3 lg:col-span-1">
          <Button
            size="sm"
            variant="outline"
            onClick={resetFilters}
          >
            {t("tasks.resetButton")}
          </Button>
        </div>
      </FilterPanel>

      <FilterSummary filtered={filteredCount} total={totalCount} unit={t("tasks.taskUnit")} />
    </>
  );
}
