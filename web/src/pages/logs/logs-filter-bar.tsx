import { useTranslation } from "react-i18next";
import { Download, Maximize2 } from "lucide-react";
import type { NodeRecord, TaskRecord } from "@/types/domain";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { InlineAlert } from "@/components/ui/inline-alert";
import { SearchInput } from "@/components/ui/search-input";
import { cn } from "@/lib/utils";

export interface LogsFilterBarProps {
  nodes: NodeRecord[];
  tasks: TaskRecord[];
  selectedNode: string;
  selectedTask: string;
  keyword: string;
  connected: boolean;
  connectionWarning: string | null;
  /** Progress value 0-100 for the focused task (or aggregate of running tasks) */
  progressValue: number;
  normalizedProgress: number;
  showProgress: boolean;
  filteredCount: number;
  totalCount: number;
  errorCode: string | undefined;
  onNodeChange: (value: string) => void;
  onTaskChange: (value: string) => void;
  onKeywordChange: (value: string) => void;
  onReset: () => void;
  onExport: () => void;
  onFullscreen: () => void;
}

export function LogsFilterBar({
  nodes,
  tasks,
  selectedNode,
  selectedTask,
  keyword,
  connected,
  connectionWarning,
  progressValue,
  normalizedProgress,
  showProgress,
  filteredCount,
  totalCount,
  errorCode,
  onNodeChange,
  onTaskChange,
  onKeywordChange,
  onReset,
  onExport,
  onFullscreen,
}: LogsFilterBarProps) {
  const { t } = useTranslation();

  return (
    <>
      <FilterPanel
        sticky={false}
        className="flex flex-wrap items-center gap-3"
      >
        <Select
          containerClassName="w-[140px]"
          aria-label={t("logs.nodeFilter")}
          value={selectedNode}
          onChange={(event) => onNodeChange(event.target.value)}
        >
          <option value="all">{t("logs.allNodes")}</option>
          {nodes.map((node) => (
            <option key={node.id} value={node.name}>
              {node.name}
            </option>
          ))}
        </Select>

        <Select
          containerClassName="w-[160px]"
          aria-label={t("logs.taskFilter")}
          value={selectedTask}
          onChange={(event) => onTaskChange(event.target.value)}
        >
          <option value="all">{t("logs.allTasks")}</option>
          {tasks.map((task) => (
            <option key={task.id} value={String(task.id)}>
              #{task.id} {task.policyName}
            </option>
          ))}
        </Select>

        <div className="hidden items-center gap-3 border-r border-border/50 pr-2 sm:flex">
          <div
            className={cn(
              "size-2.5 rounded-full shadow-sm",
              connected ? "bg-success" : "bg-muted",
            )}
            title={connected ? t("logs.connected") : t("logs.disconnected")}
          />

          {showProgress ? (
            <div
              className="flex items-center gap-2"
              title={t("logs.taskProgress")}
            >
              <span className="text-mini font-medium text-foreground/80">
                {t("logs.progress", {
                  value: Number.isFinite(progressValue) ? progressValue : 0,
                })}
              </span>
              <div
                className="h-1.5 w-16 overflow-hidden rounded-full bg-muted"
                role="progressbar"
                aria-label={t("logs.taskProgressAriaLabel")}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={normalizedProgress}
              >
                <div
                  className={cn(
                    "h-full transition-[width] duration-500",
                    normalizedProgress < 40
                      ? "bg-destructive"
                      : normalizedProgress < 70
                        ? "bg-warning"
                        : "bg-success",
                  )}
                  style={{ width: `${normalizedProgress}%` }}
                />
              </div>
            </div>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <SearchInput
            className="w-[120px] sm:w-[140px]"
            aria-label={t("logs.keywordFilter")}
            value={keyword}
            placeholder={t("logs.searchPlaceholder")}
            onChange={(event) => onKeywordChange(event.target.value)}
          />

          <Button
            size="sm"
            variant="outline"
            className="w-[80px]"
            onClick={onReset}
          >
            {t("logs.resetFilter")}
          </Button>

          <Button
            variant="outline"
            size="sm"
            className="w-[80px]"
            onClick={onExport}
          >
            <Download className="mr-1 size-3.5" />
            {t("common.export")}
          </Button>

          <Button
            variant="outline"
            size="icon"
            className="size-8"
            aria-label={t("logs.fullscreen")}
            title={t("logs.fullscreenShort")}
            onClick={onFullscreen}
          >
            <Maximize2 className="size-3.5" />
          </Button>
        </div>
      </FilterPanel>

      <FilterSummary
        filtered={filteredCount}
        total={totalCount}
        unit={t("logs.logUnit")}
      />

      <div
        className={cn(
          "overflow-hidden transition-[opacity,max-height] duration-300 ease-in-out",
          connectionWarning ? "max-h-24 opacity-100" : "max-h-0 opacity-0",
        )}
        aria-hidden={!connectionWarning}
      >
        <InlineAlert tone="warning">{connectionWarning ?? ""}</InlineAlert>
      </div>

      {errorCode ? (
        <InlineAlert tone="critical">
          {t("logs.errorCodeLabel", { code: errorCode })}
        </InlineAlert>
      ) : null}
    </>
  );
}
