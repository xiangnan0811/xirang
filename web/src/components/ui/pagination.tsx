import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";

type PaginationProps = {
  page: number;
  pageSize: number;
  total: number;
  loading?: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange?: (size: number) => void;
  pageSizeOptions?: number[];
  className?: string;
};

export function Pagination({
  page,
  pageSize,
  total,
  loading,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = [20, 50, 100],
  className,
}: PaginationProps) {
  const { t } = useTranslation();

  if (total <= 0) return null;

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const hasPrev = page > 1;
  const hasNext = page < totalPages;

  return (
    <div
      className={cn(
        "flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground",
        className,
      )}
    >
      <div className="flex items-center gap-3">
        <span>{t("common.pageInfo", { page, total })}</span>
        {onPageSizeChange ? (
          <Select
            containerClassName="w-auto"
            className="h-8 py-0 pr-7 pl-2 text-xs"
            aria-label={t("common.perPage", { size: pageSize })}
            value={String(pageSize)}
            onChange={(e) => onPageSizeChange(Number(e.target.value))}
          >
            {pageSizeOptions.map((size) => (
              <option key={size} value={String(size)}>
                {t("common.perPage", { size })}
              </option>
            ))}
          </Select>
        ) : null}
      </div>
      <div className="flex gap-2">
        <Button
          size="sm"
          variant="outline"
          onClick={() => onPageChange(Math.max(1, page - 1))}
          disabled={loading || !hasPrev}
        >
          {t("common.prevPage")}
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => onPageChange(page + 1)}
          disabled={loading || !hasNext}
        >
          {t("common.nextPage")}
        </Button>
      </div>
    </div>
  );
}
