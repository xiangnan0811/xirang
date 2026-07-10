import type { HeaderKV } from "@/types/domain";

/** Parse the backend HTTP-monitor `http_headers` JSON string into editable key/value pairs. */
export function parseHeaders(raw: string | undefined): HeaderKV[] {
  if (!raw || raw === "{}") return [];
  try {
    const obj = JSON.parse(raw);
    if (obj && typeof obj === "object") {
      return Object.entries(obj as Record<string, unknown>).map(
        ([k, v]) => ({ key: k, value: String(v) })
      );
    }
  } catch {
    // 坏 JSON：退化为空，避免整条监控解析失败
  }
  return [];
}

/** Stringify editable key/value pairs back into the backend `http_headers` JSON shape. */
export function headersToJSON(kvs: HeaderKV[]): string {
  const obj: Record<string, string> = {};
  for (const kv of kvs) {
    if (kv.key.trim()) {
      obj[kv.key.trim()] = kv.value;
    }
  }
  return JSON.stringify(obj);
}
