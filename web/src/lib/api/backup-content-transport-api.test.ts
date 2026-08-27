import { afterEach, describe, expect, it, vi } from "vitest";

import {
  BACKUP_CONTENT_TRANSPORT_KEY,
  createBackupContentTransportApi,
} from "./backup-content-transport-api";

function responseFor(value: "true" | "false", source: "db" | "env" | "default") {
  return new Response(JSON.stringify({
    code: 0,
    message: "ok",
    data: {
      definitions: [{
        key: BACKUP_CONTENT_TRANSPORT_KEY,
        env_var: "BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK",
        code_default: "false",
        type: "bool",
        category: "backup_assets",
        description: "private network transport",
      }],
      values: {
        [BACKUP_CONTENT_TRANSPORT_KEY]: { value, source, updated_at: null },
      },
    },
  }), { status: 200, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("backup content transport api", () => {
  it.each([
    ["false", "default", false],
    ["true", "env", true],
    ["false", "db", false],
  ] as const)("maps the exact boolean and %s source", async (value, source, enabled) => {
    const fetchMock = vi.fn().mockResolvedValue(responseFor(value, source));
    vi.stubGlobal("fetch", fetchMock);

    await expect(createBackupContentTransportApi().get("token")).resolves.toEqual({ enabled, source });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/settings", expect.objectContaining({
      method: "GET",
      headers: { Authorization: "Bearer token" },
    }));
  });

  it("rejects malformed or mismatched setting products", async () => {
    const malformed = responseFor("true", "default");
    const payload = await malformed.json() as { data: { definitions: Array<Record<string, unknown>> } };
    payload.data.definitions[0]!.type = "string";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(payload), { status: 200 })));

    await expect(createBackupContentTransportApi().get("token")).rejects.toThrow("invalid backup content transport setting");
  });

  it("writes only the exact key and requested boolean string", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: 0, message: "ok", data: null }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await createBackupContentTransportApi().update("token", true);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/settings", expect.objectContaining({
      method: "PUT",
      headers: { Authorization: "Bearer token", "Content-Type": "application/json" },
      body: JSON.stringify({ [BACKUP_CONTENT_TRANSPORT_KEY]: "true" }),
    }));
  });
});
