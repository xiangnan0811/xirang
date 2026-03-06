const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "/api/v1";
const DEV_DIRECT_API_BASE_URL = import.meta.env.VITE_DEV_API_DIRECT_URL ?? "http://127.0.0.1:8080/api/v1";

export class ApiError extends Error {
  status: number;
  detail?: unknown;

  constructor(status: number, message: string, detail?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.detail = detail;
  }
}

export type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  token?: string;
  signal?: AbortSignal;
};

export type Envelope<T> = {
  data?: T;
  message?: string;
  error?: string;
};

function shouldTryDirectFallback(baseUrl: string): boolean {
  if (typeof window === "undefined") {
    return false;
  }
  const isLocalhost = window.location.hostname === "localhost" || window.location.hostname === "127.0.0.1";
  return isLocalhost && baseUrl.startsWith("/");
}

async function doFetch(baseUrl: string, path: string, options: RequestOptions): Promise<Response> {
  const headers: Record<string, string> = {};
  const method = options.method ?? "GET";
  if (options.body) {
    headers["Content-Type"] = "application/json";
  }
  if (options.token) {
    headers["Authorization"] = `Bearer ${options.token}`;
  }
  return fetch(`${baseUrl}${path}`, {
    method,
    headers,
    body: options.body ? JSON.stringify(options.body) : undefined,
    signal: options.signal,
    cache: method === "GET" ? "no-store" : undefined
  });
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? "GET";
  const isWriteOperation = method !== "GET";
  let response: Response;

  try {
    response = await doFetch(API_BASE_URL, path, options);
  } catch (error) {
    if (isWriteOperation || !shouldTryDirectFallback(API_BASE_URL)) {
      throw error;
    }
    response = await doFetch(DEV_DIRECT_API_BASE_URL, path, options);
  }

  if (response.status === 404 && !isWriteOperation && shouldTryDirectFallback(API_BASE_URL)) {
    try {
      response = await doFetch(DEV_DIRECT_API_BASE_URL, path, options);
    } catch {
      // 保留原始 404 响应，避免吞掉错误上下文。
    }
  }

  const text = await response.text();
  let payload: unknown = null;

  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = text;
    }
  }

  if (!response.ok) {
    throw new ApiError(response.status, `请求失败：${response.status}`, payload);
  }

  if (payload && typeof payload === "object") {
    return payload as T;
  }

  return payload as T;
}

export async function fetchWithFallback(url: string, options: RequestInit): Promise<Response> {
  let response: Response;
  try {
    response = await fetch(`${API_BASE_URL}${url}`, options);
  } catch (error) {
    if (!shouldTryDirectFallback(API_BASE_URL)) {
      throw error;
    }
    response = await fetch(`${DEV_DIRECT_API_BASE_URL}${url}`, options);
  }

  if (response.status === 404 && shouldTryDirectFallback(API_BASE_URL)) {
    try {
      response = await fetch(`${DEV_DIRECT_API_BASE_URL}${url}`, options);
    } catch {
      // 保留原始 404 响应
    }
  }

  return response;
}

export function unwrapData<T>(payload: Envelope<T> | T): T {
  if (payload && typeof payload === "object" && "data" in (payload as Record<string, unknown>)) {
    return ((payload as Envelope<T>).data ?? null) as T;
  }
  return payload as T;
}

export function parseNumericId(rawId: string, prefix: string): number {
  const value = rawId.trim();
  if (!value) {
    throw new Error(`无效的 ${prefix} ID：不能为空`);
  }

  if (value.startsWith(`${prefix}-`)) {
    const suffix = value.slice(prefix.length + 1);
    if (/^\d+$/.test(suffix)) {
      const parsed = Number.parseInt(suffix, 10);
      if (parsed > 0) {
        return parsed;
      }
    }
  } else if (/^\d+$/.test(value)) {
    const parsed = Number.parseInt(value, 10);
    if (parsed > 0) {
      return parsed;
    }
  }

  throw new Error(`无效的 ${prefix} ID：${rawId}（期望格式：${prefix}-123 或 123）`);
}

export function formatTime(input?: string | null): string {
  if (!input) {
    return "-";
  }
  const date = new Date(input);
  if (Number.isNaN(date.getTime())) {
    return input;
  }
  return date.toLocaleString("zh-CN", { hour12: false });
}

export function extractErrorCode(message?: string): string | undefined {
  if (!message) {
    return undefined;
  }
  const matched = message.match(/XR-[A-Z]+-\d+/);
  return matched?.[0];
}
