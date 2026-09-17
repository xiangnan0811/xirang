import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { usePageFilters } from "./use-page-filters";

const config = {
  keyword: { key: "test.keyword", default: "" },
  status: { key: "test.status", default: "all" },
};

describe("usePageFilters", () => {
  beforeEach(() => localStorage.clear());

  it("absorbs initial global search before its first persistence write", () => {
    localStorage.setItem("test.keyword", "old");
    const clear = vi.fn();
    const writes = vi.spyOn(Storage.prototype, "setItem");
    const { result, rerender } = renderHook(
      ({ search }) => usePageFilters(config, search, clear),
      { initialProps: { search: "global" } },
    );
    expect(result.current.keyword).toBe("global");
    expect(writes.mock.calls.filter(([key]) => key === "test.keyword")[0]).toEqual(["test.keyword", "global"]);
    expect(clear).toHaveBeenCalledWith("");
    act(() => result.current.setKeyword("local"));
    rerender({ search: "later global" });
    expect(result.current.keyword).toBe("local");
    writes.mockRestore();
  });

  it("accepts cross-tab changes and deletion and persists reset defaults", () => {
    const { result } = renderHook(() => usePageFilters(config));
    act(() => window.dispatchEvent(new StorageEvent("storage", { key: "test.status", newValue: "failed" })));
    expect(result.current.status).toBe("failed");
    expect(result.current.isFiltered).toBe(true);
    act(() => window.dispatchEvent(new StorageEvent("storage", { key: "test.status", newValue: null })));
    expect(result.current.status).toBe("all");
    act(() => result.current.setKeyword("edited"));
    act(() => result.current.reset());
    expect(result.current.keyword).toBe("");
    expect(localStorage.getItem("test.keyword")).toBe("");
  });
});
