import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import i18n from "@/i18n";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

export function getLocale(): string {
  const localeMap: Record<string, string> = { en: "en-US", zh: "zh-CN" };
  return localeMap[i18n.language] ?? "en-US";
}

export function getErrorMessage(error: unknown, fallback = i18n.t("common.operationFailed")): string {
  if (typeof error === "string") {
    const trimmed = error.trim();
    return trimmed || fallback;
  }
  if (error instanceof Error) {
    // ApiError 的 detail 中包含后端返回的实际错误信息
    if ("detail" in error) {
      const detail = (error as Error & { detail: unknown }).detail;
      if (detail && typeof detail === "object" && "error" in detail) {
        const msg = (detail as { error: unknown }).error;
        if (typeof msg === "string" && msg.trim()) {
          return msg.trim();
        }
      }
    }
    const trimmed = error.message.trim();
    return trimmed || fallback;
  }
  return fallback;
}
