import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfigImportResultError, createConfigApi, mapConfigImportResult } from "./config-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(body)
  } as unknown as Response;
}

describe("config api", () => {
  const fetchMock = vi.fn();
  const api = createConfigApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("exportConfig 保留后端导出包裹结构，便于直接下载再导入", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        version: "1.0",
        exported_at: "2026-03-24T00:00:00Z",
        data: {
          nodes: [{ name: "node-a" }],
          tasks: [{ name: "task-a" }]
        }
      }))
    );

    const result = await api.exportConfig("auth-marker");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toMatchObject({
      version: "1.0",
      data: {
        nodes: [{ name: "node-a" }],
        tasks: [{ name: "task-a" }]
      }
    });
  });

  it("includeSecrets 导出会附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { version: "1.0", data: {} },
      }))
    );

    await api.exportConfig("auth-marker", true, "step-up-marker");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/export?include_secrets=true");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer auth-marker",
      "X-Xirang-Step-Up": "step-up-marker",
    });
  });

  it("普通导出不附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ version: "1.0", data: {} })));

    await api.exportConfig("auth-marker");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.headers).not.toHaveProperty("X-Xirang-Step-Up");
  });

  it("forwards abort signals for export and import", async () => {
    const exportController = new AbortController();
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ version: "1.0", data: {} })));
    await api.exportConfig("auth-marker", false, undefined, { signal: exportController.signal });
    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1].signal).toBe(exportController.signal);

    const importController = new AbortController();
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: { imported: 0, skipped: 0 },
    })));
    await api.importConfig("auth-marker", { nodes: [] }, "skip", "step-up-marker", { signal: importController.signal });
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/import?conflict=skip");
    expect(init.signal).toBe(importController.signal);
  });

  it("importConfig 会附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { imported: 1, skipped: 0 },
      }))
    );

    await api.importConfig("auth-marker", { ssh_keys: [] }, "skip", "step-up-marker");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/import?conflict=skip");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer auth-marker",
      "X-Xirang-Step-Up": "step-up-marker",
    });
  });

  it("importConfig 可兼容后端分项统计响应并汇总 imported", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          nodes: 1,
          ssh_keys: 2,
          policies: 3,
          tasks: 1,
          system_settings: 1
        }
      }))
    );

    const result = await api.importConfig("auth-marker", { data: { nodes: [] } }, "skip");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toEqual({
      nodes: 1,
      sshKeys: 2,
      policies: 3,
      tasks: 1,
      systemSettings: 1,
      imported: 8,
      skipped: 0,
      created: 0,
      updated: 0,
      rejected: 0,
      disabledImported: 0,
      warnings: [],
      warningsTruncated: 0,
    });
  });

  it("preserves safe counters and warning codes without raw server text", () => {
    const longName = "n".repeat(121);
    const result = mapConfigImportResult({
      nodes: 1,
      ssh_keys: 2,
      policies: 3,
      tasks: 4,
      system_settings: 5,
      imported: 9,
      skipped: 6,
      created: 1,
      updated: 2,
      rejected: 4,
      disabled_imported: 3,
      warnings_truncated: 2,
      warnings: [
        { entity: "ssh_keys", index: 0, name: "密钥-1", code: "missing_private_key", message: "SECRET_WARNING_TEXT" },
        { entity: "nodes", index: 3, code: "unresolved_ssh_key", detail: "bind exploded" },
        { entity: "policies", index: 2, name: "bad\nname", code: "invalid_input" },
        { entity: "tasks", index: 8, name: longName, code: "invalid_input" },
        { entity: "nope", index: 1, code: "missing_private_key", message: "RAW" },
        { entity: "nodes", index: 2, code: "not_a_code", message: "RAW" },
        { entity: "nodes", index: -1, code: "missing_password" },
      ],
    });

    expect(result).toEqual({
      nodes: 1,
      sshKeys: 2,
      policies: 3,
      tasks: 4,
      systemSettings: 5,
      imported: 9,
      skipped: 6,
      created: 1,
      updated: 2,
      rejected: 4,
      disabledImported: 3,
      warningsTruncated: 2,
      warnings: [
        { entity: "ssh_keys", index: 0, name: "密钥-1", code: "missing_private_key" },
        { entity: "nodes", index: 3, code: "unresolved_ssh_key" },
        { entity: "policies", index: 2, code: "invalid_input" },
        { entity: "tasks", index: 8, code: "invalid_input" },
      ],
    });
    expect(JSON.stringify(result)).not.toContain("SECRET_WARNING_TEXT");
    expect(JSON.stringify(result)).not.toContain("RAW");
    expect(JSON.stringify(result)).not.toContain("bad\nname");
  });

  it("recognizes name-mapping warning codes and drops raw server text", () => {
    const result = mapConfigImportResult({
      rejected: 3,
      warnings: [
        { entity: "nodes", index: 0, name: "web,1", code: "duplicate_name", message: "RAW_DUPLICATE", detail: "SELECT secret FROM nodes" },
        { entity: "ssh_keys", index: 1, name: "edge,key", code: "invalid_reference", error: "RAW_REFERENCE" },
        { entity: "ssh_keys", index: 2, code: "reference_conflict", reason: "RAW_CONFLICT" },
        { entity: "nodes", index: 4, code: "not_a_code", message: "RAW_UNKNOWN" },
      ],
    });

    expect(result.warnings).toEqual([
      { entity: "nodes", index: 0, name: "web,1", code: "duplicate_name" },
      { entity: "ssh_keys", index: 1, name: "edge,key", code: "invalid_reference" },
      { entity: "ssh_keys", index: 2, code: "reference_conflict" },
    ]);
    const encoded = JSON.stringify(result);
    expect(encoded).not.toContain("RAW_DUPLICATE");
    expect(encoded).not.toContain("RAW_REFERENCE");
    expect(encoded).not.toContain("RAW_CONFLICT");
    expect(encoded).not.toContain("RAW_UNKNOWN");
    expect(encoded).not.toContain("SELECT secret");
  });

  it("caps mapped warnings and ignores unsafe count types", () => {
    const warnings = Array.from({ length: 101 }, (_, index) => ({
      entity: "tasks",
      index,
      code: "invalid_input",
    }));
    const capped = mapConfigImportResult({ warnings, warnings_truncated: 4, imported: 1.5, nodes: 2, rejected: -3 });
    expect(capped.warnings).toHaveLength(100);
    expect(capped.warningsTruncated).toBe(5);
    expect(capped.imported).toBe(2);
    expect(capped.rejected).toBe(0);
  });

  it("rejects an unusable import payload", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: null })));
    await expect(api.importConfig("auth-marker", {})).rejects.toBeInstanceOf(ConfigImportResultError);
    expect(() => mapConfigImportResult([])).toThrow(ConfigImportResultError);
  });
});
