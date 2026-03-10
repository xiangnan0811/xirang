import { useDeferredValue, useCallback, useMemo, useState, useEffect } from "react";

type FilterFieldConfig<T> = {
  key: string;
  default: T;
};

type FilterConfig = Record<string, FilterFieldConfig<string>>;

type FilterValues<C extends FilterConfig> = {
  [K in keyof C]: string;
};

type FilterSetters<C extends FilterConfig> = {
  [K in keyof C as `set${Capitalize<string & K>}`]: (value: string) => void;
};

type UsePageFiltersReturn<C extends FilterConfig> = FilterValues<C> & FilterSetters<C> & {
  deferredKeyword: string;
  reset: () => void;
  isFiltered: boolean;
};

function readStorage(key: string, fallback: string): string {
  try {
    return window.localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

function writeStorage(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // ignore
  }
}

/**
 * 页面级筛选状态管理 Hook（持久化 + deferred keyword + reset）
 *
 * config 必须在组件生命周期内保持稳定（字段数量和顺序不变）。
 *
 * @example
 * const filters = usePageFilters({
 *   keyword: { key: "xirang.nodes.keyword", default: "" },
 *   status:  { key: "xirang.nodes.status",  default: "all" },
 * });
 * // filters.keyword / filters.status
 * // filters.setKeyword(v) / filters.setStatus(v)
 * // filters.deferredKeyword / filters.reset() / filters.isFiltered
 */
export function usePageFilters<C extends FilterConfig>(
  config: C,
  globalSearch?: string,
  setGlobalSearch?: (value: string) => void,
): UsePageFiltersReturn<C> {
  type StateShape = Record<string, string>;

  const entries = useMemo(() => Object.entries(config), [config]);

  const [state, setState] = useState<StateShape>(() => {
    const init: StateShape = {};
    for (const [name, field] of Object.entries(config)) {
      init[name] = readStorage(field.key, field.default);
    }
    return init;
  });

  // Sync state → localStorage
  useEffect(() => {
    for (const [name, field] of entries) {
      writeStorage(field.key, state[name] ?? field.default);
    }
  }, [entries, state]);

  // Cross-tab sync via StorageEvent
  useEffect(() => {
    const keyToName = new Map(entries.map(([name, field]) => [field.key, name]));

    const onStorage = (event: StorageEvent) => {
      if (!event.key) return;
      const name = keyToName.get(event.key);
      if (!name) return;
      const field = config[name];
      const next = event.newValue !== null ? event.newValue : field.default;
      setState((prev) => (prev[name] === next ? prev : { ...prev, [name]: next }));
    };

    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [config, entries]);

  const effectiveKeyword = state["keyword"] || globalSearch || "";
  const deferredKeyword = useDeferredValue(effectiveKeyword);

  const isFiltered = useMemo(
    () => entries.some(([name, field]) => (state[name] ?? field.default) !== field.default),
    [entries, state]
  );

  const reset = useCallback(() => {
    const defaults: StateShape = {};
    for (const [name, field] of entries) {
      defaults[name] = field.default;
    }
    setState(defaults);
    setGlobalSearch?.("");
  }, [entries, setGlobalSearch]);

  // Build the return object with individual setters
  return useMemo(() => {
    const result: Record<string, unknown> = {
      deferredKeyword,
      isFiltered,
      reset,
    };

    for (const [name] of entries) {
      result[name] = state[name];
      const setterName = `set${name.charAt(0).toUpperCase()}${name.slice(1)}`;
      result[setterName] = (value: string) => {
        setState((prev) => (prev[name] === value ? prev : { ...prev, [name]: value }));
      };
    }

    return result as UsePageFiltersReturn<C>;
  }, [deferredKeyword, entries, isFiltered, reset, state]);
}
