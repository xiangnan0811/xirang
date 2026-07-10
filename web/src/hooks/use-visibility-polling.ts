import { useEffect, useRef } from "react";

interface VisibilityPollingOptions {
  /** 是否启用轮询。默认 true。 */
  enabled?: boolean;
  /** 挂载时是否立即执行一次。默认 true。 */
  immediate?: boolean;
}

/**
 * 周期性轮询，但遵循标签页可见性：
 * - 标签页隐藏（`document.hidden`）时不发请求、不触发回调；
 * - 标签页从隐藏恢复可见时，立即补拉一次；
 * - `intervalMs <= 0` 或 `enabled === false` 时不轮询。
 *
 * 用于替换各处"无脑 30s 轮询"，避免后台标签页浪费请求与电量。
 */
export function useVisibilityPolling(
  callback: () => void,
  intervalMs: number,
  options: VisibilityPollingOptions = {}
): void {
  const { enabled = true, immediate = true } = options;
  const savedCallback = useRef(callback);

  useEffect(() => {
    savedCallback.current = callback;
  }, [callback]);

  useEffect(() => {
    if (!enabled || intervalMs <= 0) return;

    const tick = () => {
      if (typeof document !== "undefined" && document.hidden) return;
      savedCallback.current();
    };

    if (immediate) tick();

    const id = setInterval(tick, intervalMs);
    const onVisible = () => {
      if (typeof document !== "undefined" && !document.hidden) {
        savedCallback.current();
      }
    };
    document.addEventListener("visibilitychange", onVisible);

    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [enabled, intervalMs, immediate]);
}
