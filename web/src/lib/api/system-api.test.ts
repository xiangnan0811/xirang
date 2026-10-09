import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mapCronBackupStatus } from "./cron-backup-status";
import { createSystemApi } from "./system-api";

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

  const SUCCESS_RUN = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
  const OTHER_RUN = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";

  function successAttempt(artifact = "xirang-sqlite-20261007-013000.db") {
    return {
      run_id: SUCCESS_RUN,
      started_at: "2026-10-07T01:29:00Z",
      result: "success",
      finished_at: "2026-10-07T01:30:00Z",
      artifact_name: artifact,
    };
  }

  function successRecord(artifact = "xirang-sqlite-20261007-013000.db") {
    const attempt = successAttempt(artifact);
    return {
      run_id: attempt.run_id,
      started_at: attempt.started_at,
      finished_at: attempt.finished_at,
      artifact_name: attempt.artifact_name,
    };
  }

  function jobWire(overrides: Record<string, unknown> = {}) {
    return {
      evidence: "job_record",
      status: "success",
      checked_at: "2026-10-07T02:00:00.123Z",
      max_age_seconds: 93600,
      latest_attempt: successAttempt(),
      last_success: successRecord(),
      ...overrides,
    };
  }

  function emptyJob(status: string, checkedAt = "2026-10-07T02:00:00.123Z") {
    return {
      evidence: "job_record",
      status,
      checked_at: checkedAt,
      max_age_seconds: 93600,
    };
  }

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
      job: jobWire(),
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
      job: {
        evidence: "job_record",
        status: "success",
        checkedAt: "2026-10-07T02:00:00.123Z",
        maxAgeSeconds: 93600,
        latestAttempt: {
          runId: SUCCESS_RUN,
          startedAt: "2026-10-07T01:29:00Z",
          result: "success",
          finishedAt: "2026-10-07T01:30:00Z",
          artifactName: "xirang-sqlite-20261007-013000.db",
        },
        lastSuccess: {
          runId: SUCCESS_RUN,
          startedAt: "2026-10-07T01:29:00Z",
          finishedAt: "2026-10-07T01:30:00Z",
          artifactName: "xirang-sqlite-20261007-013000.db",
        },
      },
    });
    expect(mapCronBackupStatus({
      status: "not_configured",
      engine: "postgres",
      checked_at: "2026-10-07T00:00:00Z",
      max_age_seconds: 93600,
      evidence: "artifact_pair",
      time_source: "mtime",
      content_verified: false,
      job: emptyJob("not_configured", "2026-10-07T00:00:00Z"),
    })).toEqual({
      status: "not_configured",
      engine: "postgres",
      checkedAt: "2026-10-07T00:00:00Z",
      maxAgeSeconds: 93600,
      evidence: "artifact_pair",
      timeSource: "mtime",
      contentVerified: false,
      job: {
        evidence: "job_record",
        status: "not_configured",
        checkedAt: "2026-10-07T00:00:00Z",
        maxAgeSeconds: 93600,
      },
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
    ["null job", cronWire({ job: null })],
    ["job array", cronWire({ job: [] })],
    ["unknown job status", cronWire({ job: jobWire({ status: "fresh" }) })],
    ["wrong job evidence", cronWire({ job: jobWire({ evidence: "artifact_pair" }) })],
    ["job source id", cronWire({ job: jobWire({ source_id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }) })],
    ["job revision", cronWire({ job: jobWire({ revision: 4 }) })],
    ["attempt command", cronWire({ job: jobWire({ latest_attempt: { ...successAttempt(), command: "backup-db.sh" } }) })],
    ["running with artifact", cronWire({ job: jobWire({
      status: "running",
      latest_attempt: { run_id: OTHER_RUN, started_at: "2026-10-07T01:29:00Z", result: "running", artifact_name: "xirang-sqlite-20261007-013000.db" },
    }) })],
    ["failed with artifact", cronWire({ job: jobWire({
      status: "failed",
      latest_attempt: {
        run_id: OTHER_RUN,
        started_at: "2026-10-07T01:50:00Z",
        result: "failed",
        finished_at: "2026-10-07T01:51:00Z",
        failure_code: "backup_failed",
        artifact_name: "xirang-sqlite-20261007-013000.db",
      },
    }) })],
    ["interrupted with finish", cronWire({ job: jobWire({
      status: "interrupted",
      latest_attempt: {
        run_id: OTHER_RUN,
        started_at: "2026-10-07T01:50:00Z",
        result: "interrupted",
        finished_at: "2026-10-07T01:51:00Z",
        failure_code: "process_interrupted",
      },
    }) })],
    ["interrupted wrong code", cronWire({ job: jobWire({
      status: "interrupted",
      latest_attempt: {
        run_id: OTHER_RUN,
        started_at: "2026-10-07T01:50:00Z",
        result: "interrupted",
        detected_at: "2026-10-07T01:51:00Z",
        failure_code: "backup_failed",
      },
    }) })],
    ["success without last success", cronWire({ job: {
      evidence: "job_record",
      status: "success",
      checked_at: "2026-10-07T02:00:00.123Z",
      max_age_seconds: 93600,
      latest_attempt: successAttempt(),
    } })],
    ["success run mismatch", cronWire({ job: jobWire({ last_success: { ...successRecord(), run_id: OTHER_RUN } }) })],
    ["failed reuses success run", cronWire({ job: jobWire({
      status: "failed",
      latest_attempt: {
        run_id: SUCCESS_RUN,
        started_at: "2026-10-07T01:50:00Z",
        result: "failed",
        finished_at: "2026-10-07T01:51:00Z",
        failure_code: "backup_failed",
      },
    }) })],
    ["cross engine artifact", cronWire({ job: jobWire({
      latest_attempt: successAttempt("xirang-postgres-20261007-013000.dump"),
      last_success: successRecord("xirang-postgres-20261007-013000.dump"),
    }) })],
    ["impossible artifact day", cronWire({ job: jobWire({
      latest_attempt: successAttempt("xirang-sqlite-20260231-013000.db"),
      last_success: successRecord("xirang-sqlite-20260231-013000.db"),
    }) })],
    ["uppercase run id", cronWire({ job: jobWire({
      latest_attempt: { ...successAttempt(), run_id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" },
      last_success: { ...successRecord(), run_id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" },
    }) })],
    ["stale labeled success", cronWire({ job: jobWire({ checked_at: "2026-10-10T02:00:00Z" }) })],
    ["never run with success", cronWire({ job: { ...emptyJob("never_run"), last_success: successRecord() } })],
    ["invalid state with attempt", cronWire({ job: {
      ...emptyJob("state_invalid"),
      latest_attempt: { run_id: OTHER_RUN, started_at: "2026-10-07T01:29:00Z", result: "running" },
    } })],
  ])("rejects a malformed cron backup status (%s) instead of returning an empty status", (_label, payload) => {
    expect(() => mapCronBackupStatus(payload)).toThrow("invalid cron backup status");
  });

  it("maps an unknown dialect only as invalid_configuration with an empty engine", () => {
    const payload: Record<string, unknown> = cronWire({
      status: "invalid_configuration",
      engine: "",
      job: emptyJob("invalid_configuration"),
    });
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
      job: {
        evidence: "job_record",
        status: "invalid_configuration",
        checkedAt: "2026-10-07T02:00:00.123Z",
        maxAgeSeconds: 93600,
      },
    });
  });

  it("rejects a cron status that omits the required job", () => {
    const payload: Record<string, unknown> = cronWire();
    delete payload.job;
    expect(() => mapCronBackupStatus(payload)).toThrow("invalid cron backup status");
  });

  it("keeps a fresh artifact beside an unconfigured job and a success beside a missing artifact", () => {
    const unconfigured = mapCronBackupStatus(cronWire({ job: emptyJob("not_configured") }));
    expect(unconfigured.status).toBe("fresh");
    expect(unconfigured.artifactName).toBe("xirang-sqlite-20261007-013000.db");
    expect(unconfigured.job).toEqual({
      evidence: "job_record",
      status: "not_configured",
      checkedAt: "2026-10-07T02:00:00.123Z",
      maxAgeSeconds: 93600,
    });

    const row: Record<string, unknown> = cronWire({ status: "no_complete_backup" });
    delete row.latest_complete_at;
    delete row.artifact_name;
    const missing = mapCronBackupStatus(row);
    expect(missing.status).toBe("no_complete_backup");
    expect(missing.artifactName).toBeUndefined();
    expect(missing.latestCompleteAt).toBeUndefined();
    expect(missing.job.status).toBe("success");
    expect(missing.job.latestAttempt?.artifactName).toBe("xirang-sqlite-20261007-013000.db");
    expect(missing.job.lastSuccess?.artifactName).toBe("xirang-sqlite-20261007-013000.db");
  });

  it("maps a failed attempt without replacing its older success", () => {
    const mapped = mapCronBackupStatus(cronWire({
      job: jobWire({
        status: "failed",
        latest_attempt: {
          run_id: OTHER_RUN,
          started_at: "2026-10-07T01:50:00Z",
          result: "failed",
          finished_at: "2026-10-07T01:51:00Z",
          failure_code: "backup_failed",
        },
      }),
    }));
    expect(mapped.job.status).toBe("failed");
    expect(mapped.job.latestAttempt).toEqual({
      runId: OTHER_RUN,
      startedAt: "2026-10-07T01:50:00Z",
      result: "failed",
      finishedAt: "2026-10-07T01:51:00Z",
      failureCode: "backup_failed",
    });
    expect(mapped.job.lastSuccess?.runId).toBe(SUCCESS_RUN);
    expect(mapped.job.lastSuccess?.artifactName).toBe("xirang-sqlite-20261007-013000.db");
  });

  it("maps a lock-free running attempt as interrupted and keeps an unavailable future snapshot", () => {
    const interrupted = mapCronBackupStatus(cronWire({
      job: jobWire({
        status: "interrupted",
        latest_attempt: { run_id: OTHER_RUN, started_at: "2026-10-06T00:00:00Z", result: "running" },
      }),
    }));
    expect(interrupted.job.status).toBe("interrupted");
    expect(interrupted.job.latestAttempt).toEqual({
      runId: OTHER_RUN,
      startedAt: "2026-10-06T00:00:00Z",
      result: "running",
    });
    expect(interrupted.job.latestAttempt?.finishedAt).toBeUndefined();
    expect(interrupted.job.latestAttempt?.detectedAt).toBeUndefined();
    expect(interrupted.job.latestAttempt?.failureCode).toBeUndefined();
    expect(interrupted.job.lastSuccess?.runId).toBe(SUCCESS_RUN);

    const unavailable = mapCronBackupStatus(cronWire({
      job: {
        evidence: "job_record",
        status: "state_unavailable",
        checked_at: "2026-10-07T02:00:00.123Z",
        max_age_seconds: 93600,
        latest_attempt: { run_id: OTHER_RUN, started_at: "2026-10-08T00:00:00Z", result: "running" },
        last_success: successRecord(),
      },
    }));
    expect(unavailable.job.status).toBe("state_unavailable");
    expect(unavailable.job.latestAttempt).toEqual({
      runId: OTHER_RUN,
      startedAt: "2026-10-08T00:00:00Z",
      result: "running",
    });
    expect(unavailable.job.lastSuccess).toEqual({
      runId: SUCCESS_RUN,
      startedAt: "2026-10-07T01:29:00Z",
      finishedAt: "2026-10-07T01:30:00Z",
      artifactName: "xirang-sqlite-20261007-013000.db",
    });
  });

  it("accepts a clock anomaly without relabeling a future finish as success", () => {
    const futureAttempt = {
      run_id: SUCCESS_RUN,
      started_at: "2026-10-07T03:00:00Z",
      result: "success",
      finished_at: "2026-10-07T03:10:00Z",
      artifact_name: "xirang-sqlite-20261007-031000.db",
    };
    const mapped = mapCronBackupStatus(cronWire({
      status: "clock_anomaly",
      job: jobWire({
        status: "clock_anomaly",
        latest_attempt: futureAttempt,
        last_success: {
          run_id: futureAttempt.run_id,
          started_at: futureAttempt.started_at,
          finished_at: futureAttempt.finished_at,
          artifact_name: futureAttempt.artifact_name,
        },
      }),
    }));
    expect(mapped.status).toBe("clock_anomaly");
    expect(mapped.job.status).toBe("clock_anomaly");
    expect(mapped.job.latestAttempt?.result).toBe("success");
    expect(mapped.job.latestAttempt?.finishedAt).toBe("2026-10-07T03:10:00Z");

    const invertedAttempt = {
      run_id: SUCCESS_RUN,
      started_at: "2026-10-07T03:10:00Z",
      result: "success",
      finished_at: "2026-10-07T03:00:00Z",
      artifact_name: "xirang-sqlite-20261007-030000.db",
    };
    const inverted = mapCronBackupStatus(cronWire({
      job: jobWire({
        status: "clock_anomaly",
        latest_attempt: invertedAttempt,
        last_success: {
          run_id: invertedAttempt.run_id,
          started_at: invertedAttempt.started_at,
          finished_at: invertedAttempt.finished_at,
          artifact_name: invertedAttempt.artifact_name,
        },
      }),
    }));
    expect(inverted.job.status).toBe("clock_anomaly");
    expect(inverted.job.latestAttempt?.startedAt).toBe("2026-10-07T03:10:00Z");
    expect(inverted.job.latestAttempt?.finishedAt).toBe("2026-10-07T03:00:00Z");
    expect(inverted.job.lastSuccess?.finishedAt).toBe("2026-10-07T03:00:00Z");
    expect(() => mapCronBackupStatus(cronWire({
      job: jobWire({
        status: "clock_anomaly",
        latest_attempt: { ...invertedAttempt, started_at: "2026-10-07T03:10:00+00:00" },
        last_success: {
          run_id: invertedAttempt.run_id,
          started_at: "2026-10-07T03:10:00+00:00",
          finished_at: invertedAttempt.finished_at,
          artifact_name: invertedAttempt.artifact_name,
        },
      }),
    }))).toThrow("invalid cron backup status");
    expect(() => mapCronBackupStatus(cronWire({
      job: jobWire({
        status: "success",
        latest_attempt: futureAttempt,
        last_success: {
          run_id: futureAttempt.run_id,
          started_at: futureAttempt.started_at,
          finished_at: futureAttempt.finished_at,
          artifact_name: futureAttempt.artifact_name,
        },
      }),
    }))).toThrow("invalid cron backup status");
  });

  it("preserves the backend freshness boundary for running and success", () => {
    const checked = "2026-10-08T03:00:00Z";
    const started = "2026-10-07T01:00:00Z";
    const running = { run_id: OTHER_RUN, started_at: started, result: "running" };
    expect(mapCronBackupStatus(cronWire({
      checked_at: checked,
      job: { evidence: "job_record", status: "running", checked_at: checked, max_age_seconds: 93600, latest_attempt: running },
    })).job.status).toBe("running");
    expect(() => mapCronBackupStatus(cronWire({
      checked_at: checked,
      job: { evidence: "job_record", status: "overdue_running", checked_at: checked, max_age_seconds: 93600, latest_attempt: running },
    }))).toThrow("invalid cron backup status");

    const boundaryAttempt = {
      run_id: SUCCESS_RUN,
      started_at: "2026-10-07T00:59:00Z",
      result: "success",
      finished_at: started,
      artifact_name: "xirang-sqlite-20261007-010000.db",
    };
    const boundarySuccess = {
      run_id: boundaryAttempt.run_id,
      started_at: boundaryAttempt.started_at,
      finished_at: boundaryAttempt.finished_at,
      artifact_name: boundaryAttempt.artifact_name,
    };
    expect(mapCronBackupStatus(cronWire({
      checked_at: checked,
      job: {
        evidence: "job_record",
        status: "success",
        checked_at: checked,
        max_age_seconds: 93600,
        latest_attempt: boundaryAttempt,
        last_success: boundarySuccess,
      },
    })).job.status).toBe("success");
    expect(() => mapCronBackupStatus(cronWire({
      checked_at: checked,
      job: {
        evidence: "job_record",
        status: "stale",
        checked_at: checked,
        max_age_seconds: 93600,
        latest_attempt: boundaryAttempt,
        last_success: boundarySuccess,
      },
    }))).toThrow("invalid cron backup status");
  });

  it("preserves a one-nanosecond Go freshness and ordering boundary", () => {
    const maxAge = 1;
    const anchor = "2026-10-07T01:00:00Z";
    const exact = "2026-10-07T01:00:01Z";
    const before = "2026-10-07T01:00:00.999999999Z";
    const after = "2026-10-07T01:00:01.000000001Z";
    const artifact = "xirang-sqlite-20261007-010000.db";

    function runningAt(status: string, checkedAt: string, startedAt = anchor) {
      return cronWire({
        checked_at: checkedAt,
        job: {
          evidence: "job_record",
          status,
          checked_at: checkedAt,
          max_age_seconds: maxAge,
          latest_attempt: { run_id: OTHER_RUN, started_at: startedAt, result: "running" },
        },
      });
    }

    expect(mapCronBackupStatus(runningAt("running", exact)).job.status).toBe("running");
    expect(mapCronBackupStatus(runningAt("running", before)).job.status).toBe("running");
    expect(mapCronBackupStatus(runningAt("overdue_running", after)).job.status).toBe("overdue_running");
    expect(() => mapCronBackupStatus(runningAt("overdue_running", exact))).toThrow("invalid cron backup status");
    expect(() => mapCronBackupStatus(runningAt("running", after))).toThrow("invalid cron backup status");
    expect(() => mapCronBackupStatus(runningAt("overdue_running", before))).toThrow("invalid cron backup status");
    expect(mapCronBackupStatus(runningAt("running", exact, exact)).job.latestAttempt?.startedAt).toBe(exact);
    expect(() => mapCronBackupStatus(runningAt("running", exact, after))).toThrow("invalid cron backup status");

    function successAt(status: string, checkedAt: string, finishedAt: string, startedAt = "2026-10-07T00:59:00Z") {
      const attempt = {
        run_id: SUCCESS_RUN,
        started_at: startedAt,
        result: "success",
        finished_at: finishedAt,
        artifact_name: artifact,
      };
      return cronWire({
        checked_at: checkedAt,
        job: {
          evidence: "job_record",
          status,
          checked_at: checkedAt,
          max_age_seconds: maxAge,
          latest_attempt: attempt,
          last_success: {
            run_id: attempt.run_id,
            started_at: attempt.started_at,
            finished_at: attempt.finished_at,
            artifact_name: attempt.artifact_name,
          },
        },
      });
    }

    expect(mapCronBackupStatus(successAt("success", exact, anchor)).job.status).toBe("success");
    expect(mapCronBackupStatus(successAt("success", before, anchor)).job.status).toBe("success");
    expect(mapCronBackupStatus(successAt("stale", after, anchor)).job.status).toBe("stale");
    expect(() => mapCronBackupStatus(successAt("stale", exact, anchor))).toThrow("invalid cron backup status");
    expect(() => mapCronBackupStatus(successAt("success", after, anchor))).toThrow("invalid cron backup status");
    expect(() => mapCronBackupStatus(successAt("stale", before, anchor))).toThrow("invalid cron backup status");

    const startNs = "2026-10-07T01:00:00.000000002Z";
    const finishBefore = "2026-10-07T01:00:00.000000001Z";
    const finishAfter = "2026-10-07T01:00:00.000000003Z";
    const withinAge = "2026-10-07T01:00:01.000000002Z";
    expect(() => mapCronBackupStatus(successAt("success", withinAge, finishBefore, startNs))).toThrow("invalid cron backup status");
    expect(mapCronBackupStatus(successAt("success", withinAge, startNs, startNs)).job.latestAttempt?.finishedAt).toBe(startNs);
    expect(mapCronBackupStatus(successAt("success", withinAge, finishAfter, startNs)).job.latestAttempt?.finishedAt).toBe(finishAfter);
    expect(mapCronBackupStatus(successAt("success", exact, exact, anchor)).job.latestAttempt?.finishedAt).toBe(exact);
    expect(() => mapCronBackupStatus(successAt("success", exact, after, anchor))).toThrow("invalid cron backup status");

    function interruptedAt(detectedAt: string, startedAt: string) {
      return cronWire({
        checked_at: withinAge,
        job: {
          evidence: "job_record",
          status: "interrupted",
          checked_at: withinAge,
          max_age_seconds: maxAge,
          latest_attempt: {
            run_id: OTHER_RUN,
            started_at: startedAt,
            result: "interrupted",
            detected_at: detectedAt,
            failure_code: "process_interrupted",
          },
        },
      });
    }

    expect(() => mapCronBackupStatus(interruptedAt(finishBefore, startNs))).toThrow("invalid cron backup status");
    expect(mapCronBackupStatus(interruptedAt(startNs, startNs)).job.latestAttempt?.detectedAt).toBe(startNs);
    expect(() => mapCronBackupStatus(cronWire({
      checked_at: "2026-10-07T02:00:00Z",
      job: {
        evidence: "job_record",
        status: "failed",
        checked_at: "2026-10-07T02:00:00Z",
        max_age_seconds: maxAge,
        latest_attempt: {
          run_id: OTHER_RUN,
          started_at: "2026-10-07T01:30:00Z",
          result: "failed",
          finished_at: "2026-10-07T01:31:00Z",
          failure_code: "backup_failed",
        },
        last_success: {
          run_id: SUCCESS_RUN,
          started_at: startNs,
          finished_at: finishBefore,
          artifact_name: artifact,
        },
      },
    }))).toThrow("invalid cron backup status");
    expect(() => mapCronBackupStatus(runningAt("running", "2026-10-07T01:00:01.0000000001Z"))).toThrow("invalid cron backup status");
  });

  it("getCronBackupStatus reads the admin observer and rejects a malformed envelope", async () => {
    const controller = new AbortController();
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: cronWire({
        engine: "postgres",
        artifact_name: "xirang-postgres-20261007-013000.dump",
        job: jobWire({
          latest_attempt: successAttempt("xirang-postgres-20261007-013000.dump"),
          last_success: successRecord("xirang-postgres-20261007-013000.dump"),
        }),
      }),
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
