import { readFileSync } from "node:fs";
import path from "node:path";
import { runInNewContext } from "node:vm";
import { describe, expect, it, vi } from "vitest";

const workerSource = readFileSync(path.join(process.cwd(), "public/sw.js"), "utf8");
const origin = "https://xirang.example";
const assetUrl = `${origin}/assets/escalation-chunk.js`;

type CachedResponse = {
  readonly ok: boolean;
  readonly type: string;
  readonly headers: Headers;
  clone(): CachedResponse;
};

function assetResponse(contentType: string): CachedResponse {
  return {
    ok: true,
    type: "basic",
    headers: new Headers({ "Content-Type": contentType }),
    clone() { return assetResponse(contentType); },
  };
}

function startWorker() {
  const entries = new Map<string, CachedResponse>();
  const cache = {
    match: async ({ url }: { url: string }) => entries.get(url),
    put: async ({ url }: { url: string }, response: CachedResponse) => { entries.set(url, response); },
    delete: async ({ url }: { url: string }) => entries.delete(url),
  };
  const listeners = new Map<string, (event: unknown) => void>();
  const fetchAsset = vi.fn<() => Promise<CachedResponse>>();
  runInNewContext(workerSource, {
    URL,
    self: {
      location: { origin },
      addEventListener(type: string, callback: (event: unknown) => void) { listeners.set(type, callback); },
    },
    caches: { match: cache.match, open: async () => cache },
    fetch: fetchAsset,
  });

  async function requestScript() {
    let response: Promise<CachedResponse> | undefined;
    const background: Promise<unknown>[] = [];
    listeners.get("fetch")?.({
      request: { url: assetUrl, method: "GET", mode: "cors", destination: "script", headers: new Headers() },
      respondWith(value: Promise<CachedResponse>) { response = value; },
      waitUntil(value: Promise<unknown>) { background.push(value); },
    });
    if (!response) throw new Error("Script request did not reach the service worker's response path");
    const result = await response;
    await Promise.all(background);
    return result;
  }

  return { entries, fetchAsset, requestScript };
}

function responseType(response: CachedResponse | undefined): string | null | undefined {
  return response?.headers.get("Content-Type");
}

describe("script asset cache", () => {
  it("does not cache an HTML fallback and fetches the restored JavaScript", async () => {
    const worker = startWorker();
    worker.fetchAsset.mockResolvedValueOnce(assetResponse("text/html; charset=utf-8"));
    worker.fetchAsset.mockResolvedValueOnce(assetResponse("application/javascript"));

    await worker.requestScript();
    expect(worker.entries.has(assetUrl)).toBe(false);
    expect(responseType(await worker.requestScript())).toBe("application/javascript");
    expect(responseType(await worker.requestScript())).toBe("application/javascript");
    expect(worker.fetchAsset).toHaveBeenCalledTimes(2);
  });

  it("bypasses an earlier cached HTML fallback and uses the restored JavaScript", async () => {
    const worker = startWorker();
    worker.entries.set(assetUrl, assetResponse("text/html"));
    worker.fetchAsset.mockResolvedValueOnce(assetResponse("Text/JavaScript; charset=UTF-8"));

    expect(responseType(await worker.requestScript())).toBe("Text/JavaScript; charset=UTF-8");
    expect(responseType(await worker.requestScript())).toBe("Text/JavaScript; charset=UTF-8");
    expect(worker.fetchAsset).toHaveBeenCalledTimes(1);
  });
});
