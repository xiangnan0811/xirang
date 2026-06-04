import { useEffect, useMemo, useRef, useState } from "react";

export function useClientPagination<T>(items: T[], defaultPageSize = 20) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSizeRaw] = useState(defaultPageSize);
  const prevItemsRef = useRef<T[]>(items);

  const total = items.length;
  const safeTotalPages = useMemo(() => Math.max(1, Math.ceil(total / pageSize)), [total, pageSize]);

  // 当 items 变化导致当前页超出范围时，自动回退到最后一页
  // 使用 ref 比较 items 身份，避免 totalPages 变化导致的无限循环
  useEffect(() => {
    if (prevItemsRef.current === items) return;
    prevItemsRef.current = items;
    if (page > safeTotalPages) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setPage(safeTotalPages);
    }
  }, [items, page, safeTotalPages]);

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
