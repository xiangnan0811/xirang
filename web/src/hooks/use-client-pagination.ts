import { useEffect, useMemo, useState } from "react";

export function useClientPagination<T>(items: T[], defaultPageSize = 20) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSizeRaw] = useState(defaultPageSize);

  const total = items.length;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  // 当 items 变化导致当前页超出范围时，自动回退到最后一页
  useEffect(() => {
    if (page > totalPages) {
      setPage(totalPages);
    }
  }, [page, totalPages]);

  const setPageSize = (size: number) => {
    setPageSizeRaw(size);
    setPage(1);
  };

  const pagedItems = useMemo(() => {
    const start = (page - 1) * pageSize;
    return items.slice(start, start + pageSize);
  }, [items, page, pageSize]);

  return { pagedItems, page, pageSize, total, setPage, setPageSize };
}
