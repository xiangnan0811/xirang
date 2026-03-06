import { useMemo } from "react";

export type ApiResult<T> = { ok: true; data: T } | { ok: false };

type UseApiActionParams = {
  token: string | null;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

/**
 * API 写操作通用守卫。
 *
 * - 无 token → 调用 ensureDemoWriteAllowed（可能抛出），返回 null（demo 模式）
 * - API 成功 → 返回 { ok: true, data }
 * - API 失败 → 调用 handleWriteApiError（非 demo 可能抛出），返回 { ok: false }
 */
export function useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError }: UseApiActionParams) {
  return useMemo(() => {
    return async <T>(label: string, apiFn: (token: string) => Promise<T>): Promise<ApiResult<T> | null> => {
      if (!token) {
        ensureDemoWriteAllowed(label);
        return null;
      }
      try {
        return { ok: true, data: await apiFn(token) };
      } catch (error) {
        handleWriteApiError(label, error);
        return { ok: false };
      }
    };
  }, [token, ensureDemoWriteAllowed, handleWriteApiError]);
}
