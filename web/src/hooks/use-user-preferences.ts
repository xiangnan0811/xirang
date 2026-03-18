import { useSyncExternalStore } from "react";

const REFRESH_INTERVAL_KEY = "xirang.settings.refresh-interval";
const DEFAULT_PAGE_SIZE_KEY = "xirang.settings.default-page-size";
const DATETIME_FORMAT_KEY = "xirang.settings.datetime-format";

function getStorageValue(key: string, defaultValue: string): string {
  try {
    return localStorage.getItem(key) ?? defaultValue;
  } catch {
    return defaultValue;
  }
}

function createStorageHook(key: string, defaultValue: string) {
  const listeners = new Set<() => void>();
  let snapshot = getStorageValue(key, defaultValue);

  function subscribe(listener: () => void) {
    listeners.add(listener);
    return () => { listeners.delete(listener); };
  }

  function getSnapshot() {
    return snapshot;
  }

  function setValue(value: string) {
    try {
      localStorage.setItem(key, value);
    } catch { /* ignore */ }
    snapshot = value;
    listeners.forEach((l) => l());
  }

  return { subscribe, getSnapshot, setValue };
}

const refreshIntervalStore = createStorageHook(REFRESH_INTERVAL_KEY, "60");
const defaultPageSizeStore = createStorageHook(DEFAULT_PAGE_SIZE_KEY, "50");
const datetimeFormatStore = createStorageHook(DATETIME_FORMAT_KEY, "locale");

export function useRefreshInterval(): [number, (v: string) => void] {
  const raw = useSyncExternalStore(refreshIntervalStore.subscribe, refreshIntervalStore.getSnapshot);
  return [parseInt(raw, 10) || 60, refreshIntervalStore.setValue];
}

export function useDefaultPageSize(): [number, (v: string) => void] {
  const raw = useSyncExternalStore(defaultPageSizeStore.subscribe, defaultPageSizeStore.getSnapshot);
  return [parseInt(raw, 10) || 50, defaultPageSizeStore.setValue];
}

export function useDatetimeFormat(): [string, (v: string) => void] {
  const raw = useSyncExternalStore(datetimeFormatStore.subscribe, datetimeFormatStore.getSnapshot);
  return [raw, datetimeFormatStore.setValue];
}

/** 供 use-console-data.ts 调用，获取当前刷新间隔（毫秒），0 表示禁用 */
export function getRefreshIntervalMs(): number {
  const raw = getStorageValue(REFRESH_INTERVAL_KEY, "60");
  const seconds = parseInt(raw, 10);
  if (!seconds || seconds <= 0) return 0;
  return seconds * 1000;
}
