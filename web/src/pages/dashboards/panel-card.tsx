import { ChevronDown, ChevronUp, MoreVertical, Pencil, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { Panel } from "@/types/domain";
import type { PanelQueryResult } from "@/types/domain";
import { usePanelData } from "./hooks/use-panel-data";
import { PanelRenderer } from "./panel-renderer";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

type PanelCardProps = {
  panel: Panel;
  start: string;
  end: string;
  token: string;
  refreshNonce: number;
  editMode: boolean;
  onEdit?: (panel: Panel) => void;
  onDelete?: (panel: Panel) => void;
  /**
   * Wave 4 PR-C：键盘可达的"上移"兜底——react-grid-layout 拖拽默认仅支持鼠标。
   * 仅在编辑模式下出现。`undefined` 表示已是首位，不可上移。
   */
  onMoveUp?: () => void;
  /** Wave 4 PR-C：与 onMoveUp 同思路；`undefined` 表示已是末位。 */
  onMoveDown?: () => void;
};

export function PanelCard({
  panel,
  start,
  end,
  token,
  refreshNonce,
  editMode,
  onEdit,
  onDelete,
  onMoveUp,
  onMoveDown,
}: PanelCardProps) {
  const { t } = useTranslation();
  const { data, loading, error, retry } = usePanelData(
    panel,
    start,
    end,
    token,
    refreshNonce
  );

  return (
    <div className="flex h-full flex-col overflow-hidden rounded-lg border border-border bg-card shadow-sm">
      {/* 标题栏 */}
      <div className="drag-handle flex items-center justify-between border-b border-border px-3 py-2">
        <span className="truncate text-sm font-medium text-card-foreground">
          {panel.title}
        </span>
        {editMode && (
          <div className="flex items-center gap-0.5">
            {/* 键盘可达的"上移/下移"兜底，与鼠标拖拽并存。
                react-grid-layout 拖拽默认仅支持鼠标——这里给键盘用户一条平行通路。 */}
            <Button
              variant="ghost"
              size="icon"
              className="size-6 shrink-0"
              aria-label={t("dashboards.panel.moveUp")}
              disabled={!onMoveUp}
              onClick={() => onMoveUp?.()}
              // 阻止冒泡到 drag-handle 触发拖拽
              onMouseDown={(e) => e.stopPropagation()}
              onTouchStart={(e) => e.stopPropagation()}
            >
              <ChevronUp className="size-3.5" aria-hidden />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="size-6 shrink-0"
              aria-label={t("dashboards.panel.moveDown")}
              disabled={!onMoveDown}
              onClick={() => onMoveDown?.()}
              onMouseDown={(e) => e.stopPropagation()}
              onTouchStart={(e) => e.stopPropagation()}
            >
              <ChevronDown className="size-3.5" aria-hidden />
            </Button>
            <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="size-6 shrink-0"
                aria-label={t("common.more")}
              >
                <MoreVertical className="size-3.5" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => onEdit?.(panel)}>
                <Pencil className="mr-2 size-3.5" />
                {t("dashboards.panel.editButton")}
              </DropdownMenuItem>
              <DropdownMenuItem
                className="text-destructive focus:text-destructive"
                onClick={() => onDelete?.(panel)}
              >
                <Trash2 className="mr-2 size-3.5" />
                {t("dashboards.panel.deleteButton")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          </div>
        )}
      </div>

      {/* 内容区 */}
      <div className="relative flex-1 overflow-hidden p-2">
        {loading ? (
          <PanelSkeleton />
        ) : error ? (
          <PanelError message={error} onRetry={retry} />
        ) : !data || data.series.length === 0 ? (
          <PanelEmpty label={t("dashboards.panel.emptyState")} />
        ) : (
          <PanelBody data={data} panel={panel} />
        )}
      </div>
    </div>
  );
}

// ─── 子状态组件 ───────────────────────────────────────────────────

function PanelSkeleton() {
  return (
    <div className="flex h-full flex-col gap-2 p-1">
      <Skeleton className="h-4 w-3/4" />
      <Skeleton className="h-full w-full flex-1" />
    </div>
  );
}

type PanelErrorProps = { message: string; onRetry: () => void };
function PanelError({ message, onRetry }: PanelErrorProps) {
  const { t } = useTranslation();
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 rounded-md bg-destructive/10 p-3 text-center">
      <p className="text-xs text-destructive">{message}</p>
      <Button
        variant="outline"
        size="sm"
        className="text-xs"
        onClick={onRetry}
        aria-label={t("dashboards.panel.retry")}
      >
        {t("dashboards.panel.retry")}
      </Button>
    </div>
  );
}

type PanelEmptyProps = { label: string };
function PanelEmpty({ label }: PanelEmptyProps) {
  return (
    <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
      {label}
    </div>
  );
}

type PanelBodyProps = { panel: Panel; data: PanelQueryResult };
function PanelBody({ panel, data }: PanelBodyProps) {
  return (
    <div className="h-full w-full">
      <PanelRenderer panel={panel} data={data} />
    </div>
  );
}
