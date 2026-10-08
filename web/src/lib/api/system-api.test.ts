import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSystemApi, mapCronBackupStatus } from "./system-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(body)
  } as unknown as Response;
}

describe("system api", () => {
  const fetchMock = vi.fn();
  const api = createSystemApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("backupDB unwraps filename/path/size/sha256 from the response envelope", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          filename: "xirang-20260506-120000.db",
          path: "/data/backups/xirang-20260506-120000.db",
          size: 1234,
          sha256: "abc123",
        },
      }))
    );

    const result = await api.backupDB("token-1");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toEqual({
      filename: "xirang-20260506-120000.db",
      path: "/data/backups/xirang-20260506-120000.db",
      size: 1234,
      sha256: "abc123",
    });
  });

  it("maps version check and backup list wire fields to camelCase", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          update_available: true,
          current_version: "1.0.0",
          latest_version: "1.1.0",
          release_url: "https://example.com",
        },
      })),
    );
    await expect(api.checkVersion("token-1")).resolves.toEqual({
      updateAvailable: true,
      currentVersion: "1.0.0",
      latestVersion: "1.1.0",
      releaseUrl: "https://example.com",
    });

    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: [{ filename: "a.db", size: 12, created_at: "2026-01-01T00:00:00Z", sha256: "abc" }],
      })),
    );
    await expect(api.listBackups("token-1")).resolves.toEqual([
      { filename: "a.db", size: 12, createdAt: "2026-01-01T00:00:00Z", sha256: "abc" },
    ]);
  });

  it("backupDB surfaces backend envelope messages for unsupported databases", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(501, JSON.stringify({
        code: 501,
        message: "当前仅支持 SQLite 数据库备份",
        data: null,
      }))
    );

    await expect(api.backupDB("token-1")).rejects.toThrow("当前仅支持 SQLite 数据库备份");
  });

  it("forwards abort signals for backup creation and backup listing", async () => {
    const backupController = new AbortController();
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: { filename: "a.db", path: "/a.db", size: 1, sha256: "abc" },
    })));
    await api.backupDB("token-1", { signal: backupController.signal });
    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1].signal).toBe(backupController.signal);

    const listController = new AbortController();
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: [] })));
    await api.listBackups("token-1", { signal: listController.signal });
    expect((fetchMock.mock.calls[1] as [string, RequestInit])[1].signal).toBe(listController.signal);
  });

  function cronWire(overrides: Record<string, unknown> = {}) {
    return {
      status: "fresh",
      engine: "sqlite",
      checked_at: "2026-10-07T02:00:00.123Z",
      max_age_seconds: 93600,
      directory: "/backup/db",
      latest_complete_at: "2026-10-07T01:30:00Z",
      artifact_name: "xirang-sqlite-20261007-013000.db",
      evidence: "artifact_pair",
      time_source: "mtime",
      content_verified: false,
      ...overrides,
    };
  }

  it("maps a complete cron backup status to camelCase without coercing content_verified", () => {
    expect(mapCronBackupStatus(cronWire())).toEqual({
      status: "fresh",
      engine: "sqlite",
      checkedAt: "2026-10-07T02:00:00.123Z",
      maxAgeSeconds: 93600,
      directory: "/backup/db",
      latestCompleteAt: "2026-10-07T01:30:00Z",
      artifactName: "xirang-sqlite-20261007-013000.db",
      evidence: "artifact_pair",
      timeSource: "mtime",
      contentVerified: false,
    });
    expect(mapCronBackupStatus({
      status: "not_configured",
      engine: "postgres",
      checked_at: "2026-10-07T00:00:00Z",
      max_age_seconds: 93600,
      evidence: "artifact_pair",
      time_source: "mtime",
      content_verified: false,
    })).toEqual({
      status: "not_configured",
      engine: "postgres",
      checkedAt: "2026-10-07T00:00:00Z",
      maxAgeSeconds: 93600,
      evidence: "artifact_pair",
      timeSource: "mtime",
      contentVerified: false,
    });
  });

  it.each([
    ["missing object", null],
    ["array", []],
    ["empty object", {}],
    ["unknown status", cronWire({ status: "green" })],
    ["unknown engine", cronWire({ engine: "mysql" })],
    ["empty engine on a known status", cronWire({ engine: "" })],
    ["nil engine", cronWire({ engine: null })],
    ["wrong evidence", cronWire({ evidence: "sha256" })],
    ["wrong time source", cronWire({ time_source: "cron" })],
    ["verified true", cronWire({ content_verified: true })],
    ["verified string", cronWire({ content_verified: "false" })],
    ["verified number", cronWire({ content_verified: 0 })],
    ["string age", cronWire({ max_age_seconds: "93600" })],
    ["fractional age", cronWire({ max_age_seconds: 1.5 })],
    ["negative age", cronWire({ max_age_seconds: -1 })],
    ["offset timestamp", cronWire({ checked_at: "2026-10-07T02:00:00+00:00" })],
    ["impossible day", cronWire({ checked_at: "2026-02-31T00:00:00Z" })],
    ["bad pair time", cronWire({ latest_complete_at: "yesterday" })],
    ["artifact path", cronWire({ artifact_name: "/backup/db/x.db" })],
    ["artifact traversal", cronWire({ artifact_name: "../x.db" })],
    ["blank directory", cronWire({ directory: " " })],
  ])("rejects a malformed cron backup status (%s) instead of returning an empty status", (_label, payload) => {
    expect(() => mapCronBackupStatus(payload)).toThrow("invalid cron backup status");
  });

  it("maps an unknown dialect only as invalid_configuration with an empty engine", () => {
    const payload: Record<string, unknown> = cronWire({ status: "invalid_configuration", engine: "" });
    delete payload.directory;
    delete payload.latest_complete_at;
    delete payload.artifact_name;
    expect(mapCronBackupStatus(payload)).toEqual({
      status: "invalid_configuration",
      engine: "",
      checkedAt: "2026-10-07T02:00:00.123Z",
      maxAgeSeconds: 93600,
      evidence: "artifact_pair",
      timeSource: "mtime",
      contentVerified: false,
    });
  });

  it("getCronBackupStatus reads the admin observer and rejects a malformed envelope", async () => {
    const controller = new AbortController();
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: cronWire({ engine: "postgres", artifact_name: "xirang-postgres-20261007-013000.dump" }),
    })));
    await expect(api.getCronBackupStatus("token-1", { signal: controller.signal })).resolves.toMatchObject({
      engine: "postgres",
      artifactName: "xirang-postgres-20261007-013000.dump",
      contentVerified: false,
    });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/system/cron-backup-status");
    expect(init.method).toBe("GET");
    expect(init.signal).toBe(controller.signal);
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer token-1");

    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: { status: "fresh" },
    })));
    await expect(api.getCronBackupStatus("token-1")).rejects.toThrow("invalid cron backup status");
  });
});
