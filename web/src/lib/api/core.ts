import i18n from "@/i18n";

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
  code: number;
  message: string;
  data: T;
};

type LocationLike = {
  pathname: string;
  search?: string;
  hash?: string;
};

const DEFAULT_REDIRECT_TARGET = "/app/overview";

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
    cache: method === "GET" ? "no-cache" : undefined
  });
}

export function buildReturnPath(locationLike: LocationLike): string {
  const pathname = locationLike.pathname || "";
  const search = locationLike.search || "";
  const hash = locationLike.hash || "";
  const path = `${pathname}${search}${hash}`;

  if (!path || pathname === "/login") {
    return "";
  }
  return path;
}

export function buildLoginRedirectPath(locationLike: LocationLike): string {
  const returnPath = buildReturnPath(locationLike);
  if (!returnPath) {
    return "/login";
  }
  return `/login?redirect=${encodeURIComponent(returnPath)}`;
}

export function normalizeRedirectTarget(raw: string | null | undefined): string {
  const value = typeof raw === "string" ? raw.trim() : "";
  const isAppRoute = value === "/app" || value.startsWith("/app/");
  if (!value || !value.startsWith("/") || value.startsWith("//") || value.includes("\\") || !isAppRoute) {
    return DEFAULT_REDIRECT_TARGET;
  }
  return value;
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

  const AUTH_PUBLIC_PATHS = ["/auth/login", "/auth/captcha", "/auth/2fa/login"];
  if (response.status === 401 && !AUTH_PUBLIC_PATHS.includes(path)) {
    try {
      sessionStorage.removeItem("xirang-auth-token");
      sessionStorage.removeItem("xirang-username");
      sessionStorage.removeItem("xirang-role");
      sessionStorage.removeItem("xirang-user-id");
      sessionStorage.removeItem("xirang-totp-enabled");
    } catch { /* ignore */ }
    if (typeof window !== "undefined") {
      window.location.href = buildLoginRedirectPath(window.location);
    }
    throw new ApiError(401, "session expired", payload);
  }

  if (!response.ok) {
    // Try to extract message from the new envelope format
    if (payload && typeof payload === "object" && "code" in (payload as Record<string, unknown>)) {
      const envelope = payload as { code: number; message: string };
      throw new ApiError(response.status, envelope.message || i18n.t("common.requestFailed", { status: response.status }), payload);
    }
    throw new ApiError(response.status, i18n.t("common.requestFailed", { status: response.status }), payload);
  }

  // Auto-unwrap unified {code, message, data} envelope
  if (payload && typeof payload === "object" && "code" in (payload as Record<string, unknown>)) {
    const envelope = payload as { code: number; message: string; data: unknown };
    if (envelope.code !== 0) {
      throw new ApiError(envelope.code, envelope.message, payload);
    }
    // For paginated responses, return the full envelope (unwrapPaginated needs total/page/page_size)
    if ("total" in (payload as Record<string, unknown>)) {
      return payload as T;
    }
    return envelope.data as T;
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

export type PaginatedEnvelope<T> = {
  code: number;
  message: string;
  data: T;
  total: number;
  page: number;
  page_size: number;
};

export function unwrapPaginated<T>(payload: PaginatedEnvelope<T[]>): {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
} {
  return {
    items: payload.data ?? ([] as T[]),
    total: Number(payload.total ?? 0),
    page: Number(payload.page ?? 1),
    pageSize: Number(payload.page_size ?? 20),
  };
}

export function parseNumericId(rawId: string, prefix: string): number {
  const value = rawId.trim();
  if (!value) {
    throw new Error(i18n.t("common.invalidIdEmpty", { prefix }));
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

  throw new Error(i18n.t("common.invalidIdFormat", { prefix, rawId }));
}

function pad(n: number): string {
  return n.toString().padStart(2, "0");
}

// 统一时间显示格式 YYYY-MM-DD HH:mm:ss，语言无关（中英文一致），本地时区。
export function formatTime(input?: string | null): string {
  if (!input) return "-";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return input;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function formatDateOnly(input?: string | null): string {
  if (!input) return "-";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return input;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function formatTimeOnly(input?: string | null): string {
  if (!input) return "-";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return input;
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function extractErrorCode(message?: string): string | undefined {
  if (!message) {
    return undefined;
  }
  const matched = message.match(/XR-[A-Z]+-\d+/);
  return matched?.[0];
}
