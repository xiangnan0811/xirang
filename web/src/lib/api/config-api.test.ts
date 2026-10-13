import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ConfigImportPreviewError,
  ConfigImportResultError,
  createConfigApi,
  mapConfigImportPreview,
  mapConfigImportResult,
} from "./config-api";

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

  function previewEntity(
    entity: string,
    counts: Partial<Record<"input" | "created" | "updated" | "skipped" | "rejected" | "pending_validation" | "disabled_imported", number>> = {},
  ) {
    return {
      entity,
      input: 0,
      created: 0,
      updated: 0,
      skipped: 0,
      rejected: 0,
      pending_validation: 0,
      disabled_imported: 0,
      ...counts,
    };
  }

  function previewAsset(entity: string, input = 0, status = input === 0 ? "not_present" : "unknown") {
    return { entity, input, status };
  }

  function completePreviewFixture(conflict: "skip" | "overwrite" = "skip") {
    return {
      conflict,
      basis: "current_snapshot",
      entities: [
        previewEntity("nodes", { input: 2, created: 1, skipped: 1 }),
        previewEntity("ssh_keys", { input: 1, pending_validation: 1 }),
        previewEntity("policies", { input: 1, rejected: 1 }),
        previewEntity("tasks", { input: 1, updated: 1, disabled_imported: 1 }),
        previewEntity("system_settings", { input: 1, updated: 1 }),
      ],
      warnings: [
        { entity: "nodes", index: 1, name: "edge-1", code: "duplicate_name", message: "RAW_WARNING", detail: "SELECT secret" },
      ],
      pending_checks: [
        { entity: "ssh_keys", index: 0, code: "retained_secret", message: "RAW_PENDING" },
      ],
      pending_checks_truncated: 0,
      warnings_truncated: 0,
      system_settings: { will_write: true, created: 0, updated: 1, applies_in_skip: true, value: "SECRET_SETTING" },
      secrets: [
        { entity: "nodes", index: 0, field: "password", present: true, action: "supplied", fingerprint: "SECRET_FINGERPRINT" },
        { entity: "nodes", index: 0, field: "private_key", present: false, action: "missing" },
        { entity: "ssh_keys", index: 0, field: "private_key", present: false, action: "preserved" },
      ],
      secrets_truncated: 0,
      asset_graph: [
        previewAsset("backup_repositories", 1, "unknown"),
        previewAsset("task_repository_links"),
        previewAsset("backup_retention_policies"),
        previewAsset("recovery_point_holds"),
      ],
      requires_apply_validation: true,
      proof: "GRANT_SECRET",
      plan_token: "PLAN_SECRET",
    };
  }

  it("maps a complete backend preview without proof, grant, or unknown fields", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: completePreviewFixture("overwrite"),
    })));
    const controller = new AbortController();

    const result = await api.previewConfig("auth-marker", { nodes: [{ name: "edge" }] }, "overwrite", {
      signal: controller.signal,
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/import/preview?conflict=overwrite");
    expect(init.method).toBe("POST");
    expect(init.signal).toBe(controller.signal);
    expect(init.body).toBe(JSON.stringify({ nodes: [{ name: "edge" }] }));
    expect(init.headers).toMatchObject({ Authorization: "Bearer auth-marker" });
    expect(init.headers).not.toHaveProperty("X-Xirang-Step-Up");
    expect(JSON.stringify(init.headers)).not.toMatch(/grant|proof|step-up/i);
    expect(result).toEqual({
      conflict: "overwrite",
      basis: "current_snapshot",
      entities: [
        { entity: "nodes", input: 2, created: 1, updated: 0, skipped: 1, rejected: 0, pendingValidation: 0, disabledImported: 0 },
        { entity: "ssh_keys", input: 1, created: 0, updated: 0, skipped: 0, rejected: 0, pendingValidation: 1, disabledImported: 0 },
        { entity: "policies", input: 1, created: 0, updated: 0, skipped: 0, rejected: 1, pendingValidation: 0, disabledImported: 0 },
        { entity: "tasks", input: 1, created: 0, updated: 1, skipped: 0, rejected: 0, pendingValidation: 0, disabledImported: 1 },
        { entity: "system_settings", input: 1, created: 0, updated: 1, skipped: 0, rejected: 0, pendingValidation: 0, disabledImported: 0 },
      ],
      warnings: [{ entity: "nodes", index: 1, name: "edge-1", code: "duplicate_name" }],
      pendingChecks: [{ entity: "ssh_keys", index: 0, code: "retained_secret" }],
      pendingChecksTruncated: 0,
      warningsTruncated: 0,
      systemSettings: { willWrite: true, created: 0, updated: 1, appliesInSkip: true },
      secrets: [
        { entity: "nodes", index: 0, field: "password", present: true, action: "supplied" },
        { entity: "nodes", index: 0, field: "private_key", present: false, action: "missing" },
        { entity: "ssh_keys", index: 0, field: "private_key", present: false, action: "preserved" },
      ],
      secretsTruncated: 0,
      assetGraph: [
        { entity: "backup_repositories", input: 1, status: "unknown" },
        { entity: "task_repository_links", input: 0, status: "not_present" },
        { entity: "backup_retention_policies", input: 0, status: "not_present" },
        { entity: "recovery_point_holds", input: 0, status: "not_present" },
      ],
      requiresApplyValidation: true,
    });
    const encoded = JSON.stringify(result);
    expect(encoded).not.toContain("RAW_WARNING");
    expect(encoded).not.toContain("RAW_PENDING");
    expect(encoded).not.toContain("SELECT secret");
    expect(encoded).not.toContain("SECRET_SETTING");
    expect(encoded).not.toContain("SECRET_FINGERPRINT");
    expect(encoded).not.toContain("GRANT_SECRET");
    expect(encoded).not.toContain("PLAN_SECRET");
  });

  it("defaults preview conflict to skip and never sends proof or grant", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: completePreviewFixture(),
    })));

    await api.previewConfig("auth-marker", { nodes: [] });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/import/preview?conflict=skip");
    expect(init.headers).not.toHaveProperty("X-Xirang-Step-Up");
    expect(JSON.stringify(init)).not.toMatch(/grant|proof|step-up/i);
  });

  it("rejects malformed, partial, reordered, and unsafe preview responses", () => {
    const fixture = completePreviewFixture();
    const reject = (payload: unknown) => {
      let thrown: unknown;
      try {
        mapConfigImportPreview(payload, "skip");
      } catch (error) {
        thrown = error;
      }
      expect(thrown).toBeInstanceOf(ConfigImportPreviewError);
      expect(thrown).toMatchObject({
        name: "ConfigImportPreviewError",
        message: "config import preview is unusable",
        code: "CONFIG_IMPORT_PREVIEW_UNUSABLE",
      });
      expect(thrown).not.toHaveProperty("detail");
      const message = thrown instanceof Error ? thrown.message : "";
      expect(message).not.toMatch(/RAW|SECRET|SELECT|GRANT/);
    };

    reject(null);
    reject([]);
    reject({ ...fixture, conflict: "overwrite" });
    reject({ ...fixture, basis: "planned" });
    reject({ ...fixture, requires_apply_validation: false });
    reject({ ...fixture, entities: fixture.entities.slice(0, 4) });
    reject({ ...fixture, entities: [fixture.entities[1], fixture.entities[0], ...fixture.entities.slice(2)] });
    reject({
      ...fixture,
      entities: fixture.entities.map((entity) => entity.entity === "nodes" ? { ...entity, created: undefined } : entity),
    });
    reject({
      ...fixture,
      entities: fixture.entities.map((entity) => entity.entity === "ssh_keys" ? { ...entity, pending_validation: -1 } : entity),
    });
    reject({
      ...fixture,
      entities: fixture.entities.map((entity) => entity.entity === "tasks" ? { ...entity, input: 1.5 } : entity),
    });
    reject({
      ...fixture,
      entities: fixture.entities.map((entity) => entity.entity === "policies" ? { ...entity, rejected: Number.MAX_SAFE_INTEGER + 1 } : entity),
    });
    reject({ ...fixture, pending_checks: [{ entity: "tasks", index: 0, code: "missing_private_key", detail: "RAW" }] });
    reject({ ...fixture, pending_checks: [{ entity: "nope", index: 0, code: "retained_secret" }] });
    reject({ ...fixture, secrets: [{ entity: "policies", index: 0, field: "private_key", present: true, action: "supplied" }] });
    reject({ ...fixture, secrets: [{ entity: "ssh_keys", index: 0, field: "password", present: true, action: "supplied" }] });
    reject({ ...fixture, secrets: [{ entity: "nodes", index: 0, field: "private_key", present: "yes", action: "supplied" }] });
    reject({ ...fixture, secrets: [{ entity: "nodes", index: 0, field: "private_key", present: false, action: "created" }] });
    reject({ ...fixture, asset_graph: fixture.asset_graph.slice(0, 3) });
    reject({
      ...fixture,
      asset_graph: fixture.asset_graph.map((asset) => asset.entity === "backup_repositories" ? { ...asset, input: 1, status: "not_present" } : asset),
    });
    reject({
      ...fixture,
      asset_graph: fixture.asset_graph.map((asset) => asset.entity === "task_repository_links" ? { ...asset, input: 0, status: "unknown" } : asset),
    });
    reject({
      ...fixture,
      asset_graph: fixture.asset_graph.map((asset) => asset.entity === "recovery_point_holds" ? { ...asset, status: "ready" } : asset),
    });
    reject({ ...fixture, system_settings: { will_write: false, created: 0, updated: 1, applies_in_skip: true } });
    reject({ ...fixture, system_settings: { will_write: true, created: 1, updated: 1, applies_in_skip: true } });
    reject({ ...fixture, system_settings: { will_write: true, created: 0, updated: 1, applies_in_skip: false } });
    reject({ ...fixture, warnings_truncated: -1 });
    reject({ ...fixture, pending_checks: null });
    reject({ ...fixture, secrets: { entity: "nodes" } });

    const accepted = mapConfigImportPreview({
      ...fixture,
      warnings: [
        { entity: "nodes", index: 0, code: "not_a_code", message: "RAW_DROPPED_WARNING" },
        { entity: "policies", index: 2, name: "bad\nname", code: "invalid_input", detail: "SELECT secret" },
      ],
      asset_graph: fixture.asset_graph.map((asset) => asset.entity === "backup_repositories"
        ? { ...asset, input: 2, status: "unsupported" }
        : asset),
    }, "skip");
    expect(accepted.warnings).toEqual([{ entity: "policies", index: 2, code: "invalid_input" }]);
    expect(accepted.warningsTruncated).toBe(0);
    expect(accepted.assetGraph[0]).toEqual({ entity: "backup_repositories", input: 2, status: "unsupported" });
    const acceptedEncoded = JSON.stringify(accepted);
    expect(acceptedEncoded).not.toContain("RAW_DROPPED_WARNING");
    expect(acceptedEncoded).not.toContain("bad\nname");
    expect(acceptedEncoded).not.toContain("SELECT secret");
    expect(acceptedEncoded).not.toContain("GRANT_SECRET");
    expect(acceptedEncoded).not.toContain("SECRET_SETTING");
  });

  it("rejects a preview response that omits a required count instead of treating it as zero", () => {
    const fixture = completePreviewFixture();
    const entities = fixture.entities.map((entity) => {
      if (entity.entity !== "nodes") return entity;
      return {
        entity: entity.entity,
        input: entity.input,
        created: entity.created,
        updated: entity.updated,
        rejected: entity.rejected,
        pending_validation: entity.pending_validation,
        disabled_imported: entity.disabled_imported,
      };
    });

    expect(() => mapConfigImportPreview({ ...fixture, entities }, "skip")).toThrow(ConfigImportPreviewError);
  });

  it("caps preview lists and adds locally dropped rows to the truncated counters", () => {
    const fixture = completePreviewFixture();
    const warnings = Array.from({ length: 101 }, (_, index) => ({ entity: "tasks", index, code: "invalid_input" }));
    const pendingChecks = Array.from({ length: 101 }, (_, index) => ({ entity: "tasks", index, code: "allocated_identity" }));
    const secrets = Array.from({ length: 101 }, (_, index) => ({
      entity: "nodes",
      index,
      field: "password",
      present: false,
      action: "missing",
    }));

    const result = mapConfigImportPreview({
      ...fixture,
      warnings,
      pending_checks: pendingChecks,
      secrets,
      warnings_truncated: 4,
      pending_checks_truncated: 2,
      secrets_truncated: 3,
    }, "skip");

    expect(result.warnings).toHaveLength(100);
    expect(result.pendingChecks).toHaveLength(100);
    expect(result.secrets).toHaveLength(100);
    expect(result.warningsTruncated).toBe(5);
    expect(result.pendingChecksTruncated).toBe(3);
    expect(result.secretsTruncated).toBe(4);
    expect(result.pendingChecks[0]?.code).toBe("allocated_identity");
  });

  it("rejects an unusable preview payload from the request boundary", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: { conflict: "skip" } })));

    await expect(api.previewConfig("auth-marker", { nodes: [] })).rejects.toBeInstanceOf(ConfigImportPreviewError);
  });

  it("leaves preview authorization failures on the request boundary", async () => {
    fetchMock.mockResolvedValueOnce({
      status: 403,
      ok: false,
      headers: { get: () => null },
      text: vi.fn().mockResolvedValue(JSON.stringify({
        code: 403,
        message: "forbidden",
        data: { error_code: "FORBIDDEN", detail: "SECRET_POLICY" },
      })),
    });

    await expect(api.previewConfig("auth-marker", { nodes: [] }, "overwrite")).rejects.toMatchObject({
      name: "ApiError",
      status: 403,
    });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/import/preview?conflict=overwrite");
    expect(init.headers).toMatchObject({ Authorization: "Bearer auth-marker" });
    expect(init.headers).not.toHaveProperty("X-Xirang-Step-Up");
    expect(JSON.stringify(init.headers)).not.toMatch(/grant|proof|step-up/i);
  });

  it("propagates preview request cancellation without mapping a body", async () => {
    const controller = new AbortController();
    const abortError = new DOMException("The operation was aborted.", "AbortError");
    fetchMock.mockRejectedValueOnce(abortError);

    await expect(api.previewConfig("auth-marker", { nodes: [] }, "skip", { signal: controller.signal })).rejects.toBe(abortError);
    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1].signal).toBe(controller.signal);
  });
});
