import { request } from "./core";

export const CRON_BACKUP_STATUSES = [
  "not_configured",
  "invalid_configuration",
  "directory_unreadable",
  "no_complete_backup",
  "scan_limit_exceeded",
  "clock_anomaly",
  "stale",
  "fresh",
] as const;

export type CronBackupStatusCode = (typeof CRON_BACKUP_STATUSES)[number];
export type CronBackupEngine = "sqlite" | "postgres";

export const CRON_JOB_STATUSES = [
  "not_configured",
  "invalid_configuration",
  "not_initialized",
  "state_unavailable",
  "state_invalid",
  "clock_anomaly",
  "never_run",
  "running",
  "overdue_running",
  "interrupted",
  "failed",
  "success",
  "stale",
] as const;

export type CronJobStatusCode = (typeof CRON_JOB_STATUSES)[number];
export type CronAttemptResult = "running" | "success" | "failed" | "interrupted";
export type CronFailureCode = "backup_failed" | "backup_start_failed" | "result_invalid" | "process_interrupted";

export type CronJobAttempt = {
  runId: string;
  startedAt: string;
  result: CronAttemptResult;
  finishedAt?: string;
  detectedAt?: string;
  failureCode?: CronFailureCode;
  artifactName?: string;
};

export type CronJobSuccess = {
  runId: string;
  startedAt: string;
  finishedAt: string;
  artifactName: string;
};

export type CronJobObservation = {
  evidence: "job_record";
  status: CronJobStatusCode;
  checkedAt: string;
  maxAgeSeconds: number;
  latestAttempt?: CronJobAttempt;
  lastSuccess?: CronJobSuccess;
};

export type CronBackupStatus = {
  status: CronBackupStatusCode;
  /** Empty only when status is invalid_configuration and the backend dialect is unknown. */
  engine: CronBackupEngine | "";
  checkedAt: string;
  maxAgeSeconds: number;
  directory?: string;
  latestCompleteAt?: string;
  artifactName?: string;
  evidence: "artifact_pair";
  timeSource: "mtime";
  contentVerified: false;
  job: CronJobObservation;
};

const CRON_BACKUP_STATUS_SET: Record<string, true> = Object.fromEntries(
  CRON_BACKUP_STATUSES.map((status) => [status, true]),
);
const RFC3339_UTC = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d{1,9})?Z$/;
const NANOSECONDS_PER_MILLISECOND = 1_000_000n;
const NANOSECONDS_PER_SECOND = 1_000_000_000n;
const INVALID_CRON_BACKUP_STATUS = "invalid cron backup status";

function invalidCronBackupStatus(): never {
  throw new Error(INVALID_CRON_BACKUP_STATUS);
}

function isCronBackupRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function isCronBackupStatusCode(value: unknown): value is CronBackupStatusCode {
  return typeof value === "string" && CRON_BACKUP_STATUS_SET[value] === true;
}

function isRfc3339Utc(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const match = RFC3339_UTC.exec(value);
  if (!match) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59) return false;
  const probe = new Date(Date.UTC(year, month - 1, day));
  return probe.getUTCFullYear() === year && probe.getUTCMonth() === month - 1 && probe.getUTCDate() === day;
}

function requiredUtc(value: unknown): string {
  if (!isRfc3339Utc(value)) invalidCronBackupStatus();
  return value;
}

// Whole-second UTC epoch plus the fractional digits padded to nine places.
// Date.parse stops at milliseconds, which both rejects a valid Go max_age+1ns
// status and accepts a finish that precedes its start by 1ns.
function utcEpochNanos(value: string): bigint {
  const match = RFC3339_UTC.exec(value);
  if (!match) invalidCronBackupStatus();
  const wholeMillis = Date.UTC(
    Number(match[1]),
    Number(match[2]) - 1,
    Number(match[3]),
    Number(match[4]),
    Number(match[5]),
    Number(match[6]),
  );
  if (!Number.isSafeInteger(wholeMillis)) invalidCronBackupStatus();
  const fraction = match[7] ? match[7].slice(1).padEnd(9, "0") : "000000000";
  return BigInt(wholeMillis) * NANOSECONDS_PER_MILLISECOND + BigInt(fraction);
}

function optionalUtc(row: Record<string, unknown>, key: string): string | undefined {
  if (!Object.hasOwn(row, key)) return undefined;
  const value = row[key];
  if (!isRfc3339Utc(value)) invalidCronBackupStatus();
  return value;
}

function optionalDirectory(row: Record<string, unknown>): string | undefined {
  if (!Object.hasOwn(row, "directory")) return undefined;
  const value = row.directory;
  if (typeof value !== "string" || value.length === 0 || value !== value.trim() || value.includes("\0")) {
    invalidCronBackupStatus();
  }
  return value;
}

function optionalArtifactName(row: Record<string, unknown>): string | undefined {
  if (!Object.hasOwn(row, "artifact_name")) return undefined;
  const value = row.artifact_name;
  if (typeof value !== "string" || value.length === 0 || value !== value.trim()) invalidCronBackupStatus();
  if (value.includes("/") || value.includes("\\") || value.includes("\0") || value === "." || value === "..") {
    invalidCronBackupStatus();
  }
  return value;
}

function requiredMaxAgeSeconds(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) invalidCronBackupStatus();
  return value;
}

const CRON_JOB_STATUS_SET: Record<string, true> = Object.fromEntries(CRON_JOB_STATUSES.map((status) => [status, true]));
const JOB_FIELDS: Record<string, true> = {
  evidence: true, status: true, checked_at: true, max_age_seconds: true, latest_attempt: true, last_success: true,
};
const ATTEMPT_FIELDS: Record<string, true> = {
  run_id: true, started_at: true, result: true, finished_at: true, detected_at: true, failure_code: true, artifact_name: true,
};
const SUCCESS_FIELDS: Record<string, true> = {
  run_id: true, started_at: true, finished_at: true, artifact_name: true,
};
const EMPTY_JOB_STATUSES: Record<string, true> = {
  not_configured: true,
  invalid_configuration: true,
  not_initialized: true,
  state_invalid: true,
  never_run: true,
};
const OPTIONAL_ATTEMPT_STATUSES: Record<string, true> = { state_unavailable: true, clock_anomaly: true };
// Passive observations keep the recorded timestamps. clock_anomaly explains a future or
// out-of-order instant, and state_unavailable can be returned before that classification.
const UNORDERED_JOB_STATUSES: Record<string, true> = { state_unavailable: true, clock_anomaly: true };
const RUN_ID = /^[0-9a-f]{32}$/;
const ARTIFACT_NAME = /^xirang-(sqlite|postgres)-(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})\.(db|dump)$/;

function isJobStatus(value: unknown): value is CronJobStatusCode {
  return typeof value === "string" && CRON_JOB_STATUS_SET[value] === true;
}

function isAttemptResult(value: unknown): value is CronAttemptResult {
  return value === "running" || value === "success" || value === "failed" || value === "interrupted";
}

function isFailedCode(value: unknown): value is Exclude<CronFailureCode, "process_interrupted"> {
  return value === "backup_failed" || value === "backup_start_failed" || value === "result_invalid";
}

function closedRecord(value: unknown, allowed: Record<string, true>): Record<string, unknown> {
  if (!isCronBackupRecord(value)) invalidCronBackupStatus();
  for (const key of Object.keys(value)) {
    if (allowed[key] !== true) invalidCronBackupStatus();
  }
  return value;
}

function hasAnyKey(row: Record<string, unknown>, keys: readonly string[]): boolean {
  return keys.some((key) => Object.hasOwn(row, key));
}

function isSafeArtifact(name: string, engine: CronBackupEngine): boolean {
  const match = ARTIFACT_NAME.exec(name);
  if (!match) return false;
  const artifactEngine = match[1];
  const suffix = match[8];
  if (artifactEngine !== engine) return false;
  if (engine === "sqlite" && suffix !== "db") return false;
  if (engine === "postgres" && suffix !== "dump") return false;
  const year = Number(match[2]);
  const month = Number(match[3]);
  const day = Number(match[4]);
  const hour = Number(match[5]);
  const minute = Number(match[6]);
  const second = Number(match[7]);
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59) return false;
  const probe = new Date(Date.UTC(year, month - 1, day));
  return probe.getUTCFullYear() === year && probe.getUTCMonth() === month - 1 && probe.getUTCDate() === day;
}

function attemptMatchesStatus(status: CronJobStatusCode, result: CronAttemptResult): boolean {
  switch (status) {
    case "running":
    case "overdue_running":
      return result === "running";
    case "interrupted":
      return result === "running" || result === "interrupted";
    case "failed":
      return result === "failed";
    case "success":
    case "stale":
      return result === "success";
    case "clock_anomaly":
    case "state_unavailable":
      return true;
    default:
      return false;
  }
}

function mapAttempt(value: unknown, engine: CronBackupEngine | ""): CronJobAttempt {
  const row = closedRecord(value, ATTEMPT_FIELDS);
  if (typeof row.run_id !== "string" || !RUN_ID.test(row.run_id) || !isAttemptResult(row.result)) invalidCronBackupStatus();
  const attempt: CronJobAttempt = { runId: row.run_id, startedAt: requiredUtc(row.started_at), result: row.result };
  if (attempt.result === "running") {
    if (hasAnyKey(row, ["finished_at", "detected_at", "failure_code", "artifact_name"])) invalidCronBackupStatus();
    return attempt;
  }
  if (attempt.result === "success") {
    if (hasAnyKey(row, ["detected_at", "failure_code"]) || !Object.hasOwn(row, "finished_at") || !Object.hasOwn(row, "artifact_name")) {
      invalidCronBackupStatus();
    }
    if (typeof row.artifact_name !== "string" || engine === "" || !isSafeArtifact(row.artifact_name, engine)) invalidCronBackupStatus();
    attempt.finishedAt = requiredUtc(row.finished_at);
    attempt.artifactName = row.artifact_name;
    return attempt;
  }
  if (attempt.result === "failed") {
    if (hasAnyKey(row, ["detected_at", "artifact_name"]) || !Object.hasOwn(row, "finished_at") || !isFailedCode(row.failure_code)) {
      invalidCronBackupStatus();
    }
    attempt.finishedAt = requiredUtc(row.finished_at);
    attempt.failureCode = row.failure_code;
    return attempt;
  }
  if (hasAnyKey(row, ["finished_at", "artifact_name"]) || !Object.hasOwn(row, "detected_at") || row.failure_code !== "process_interrupted") {
    invalidCronBackupStatus();
  }
  attempt.detectedAt = requiredUtc(row.detected_at);
  attempt.failureCode = "process_interrupted";
  return attempt;
}

function mapSuccess(value: unknown, engine: CronBackupEngine | ""): CronJobSuccess {
  const row = closedRecord(value, SUCCESS_FIELDS);
  if (typeof row.run_id !== "string" || !RUN_ID.test(row.run_id)) invalidCronBackupStatus();
  if (typeof row.artifact_name !== "string" || engine === "" || !isSafeArtifact(row.artifact_name, engine)) invalidCronBackupStatus();
  return {
    runId: row.run_id,
    startedAt: requiredUtc(row.started_at),
    finishedAt: requiredUtc(row.finished_at),
    artifactName: row.artifact_name,
  };
}

function sameSuccess(attempt: CronJobAttempt, success: CronJobSuccess): boolean {
  return attempt.runId === success.runId
    && attempt.startedAt === success.startedAt
    && attempt.finishedAt === success.finishedAt
    && attempt.artifactName === success.artifactName;
}

function enforceAttemptTiming(
  status: CronJobStatusCode,
  attempt: CronJobAttempt,
  success: CronJobSuccess | undefined,
  checkedAt: string,
  maxAgeSeconds: number,
): void {
  if (UNORDERED_JOB_STATUSES[status] === true) return;
  const checked = utcEpochNanos(checkedAt);
  const started = utcEpochNanos(attempt.startedAt);
  if (started > checked) invalidCronBackupStatus();
  let finished: bigint | undefined;
  if (attempt.finishedAt) {
    finished = utcEpochNanos(attempt.finishedAt);
    if (finished < started || finished > checked) invalidCronBackupStatus();
  }
  if (attempt.detectedAt) {
    const detected = utcEpochNanos(attempt.detectedAt);
    if (detected < started || detected > checked) invalidCronBackupStatus();
  }
  if (success) {
    const successStarted = utcEpochNanos(success.startedAt);
    const successFinished = utcEpochNanos(success.finishedAt);
    if (successFinished < successStarted || successStarted > checked || successFinished > checked) invalidCronBackupStatus();
  }
  const maxAgeNanos = BigInt(maxAgeSeconds) * NANOSECONDS_PER_SECOND;
  if (status === "running" || status === "overdue_running") {
    const overdue = checked - started > maxAgeNanos;
    if ((status === "overdue_running") !== overdue) invalidCronBackupStatus();
  }
  if (status === "success" || status === "stale") {
    if (finished === undefined) invalidCronBackupStatus();
    const stale = checked - finished > maxAgeNanos;
    if ((status === "stale") !== stale) invalidCronBackupStatus();
  }
}

// Closed job contract: missing job, unknown fields, and illegal attempt combinations
// fail the read. Source identity, revision, lock paths, and raw errors are never copied.
// A lock-free running attempt is interrupted without inventing detected_at or a failure
// code. clock_anomaly and state_unavailable keep the recorded instants, including future
// or out-of-order ones. Every other status is ordered and aged in nanoseconds, and a
// timestamp that is not RFC3339 UTC with at most nine fractional digits is rejected.
function mapJob(value: unknown, engine: CronBackupEngine | ""): CronJobObservation {
  const row = closedRecord(value, JOB_FIELDS);
  if (row.evidence !== "job_record" || !isJobStatus(row.status)) invalidCronBackupStatus();
  const job: CronJobObservation = {
    evidence: "job_record",
    status: row.status,
    checkedAt: requiredUtc(row.checked_at),
    maxAgeSeconds: requiredMaxAgeSeconds(row.max_age_seconds),
  };
  const hasAttempt = Object.hasOwn(row, "latest_attempt");
  const hasSuccess = Object.hasOwn(row, "last_success");
  if (engine === "" && (hasAttempt || hasSuccess)) invalidCronBackupStatus();
  if (EMPTY_JOB_STATUSES[job.status] === true) {
    if (hasAttempt || hasSuccess) invalidCronBackupStatus();
    return job;
  }
  if (!hasAttempt) {
    if (OPTIONAL_ATTEMPT_STATUSES[job.status] !== true || hasSuccess) invalidCronBackupStatus();
    return job;
  }
  const attempt = mapAttempt(row.latest_attempt, engine);
  if (!attemptMatchesStatus(job.status, attempt.result)) invalidCronBackupStatus();
  let success: CronJobSuccess | undefined;
  if (hasSuccess) {
    success = mapSuccess(row.last_success, engine);
    if (attempt.result === "success" ? !sameSuccess(attempt, success) : success.runId === attempt.runId) invalidCronBackupStatus();
  } else if (attempt.result === "success") {
    invalidCronBackupStatus();
  }
  enforceAttemptTiming(job.status, attempt, success, job.checkedAt, job.maxAgeSeconds);
  job.latestAttempt = attempt;
  if (success) job.lastSuccess = success;
  return job;
}

export function mapCronBackupStatus(raw: unknown): CronBackupStatus {
  if (!isCronBackupRecord(raw)) invalidCronBackupStatus();
  if (!isCronBackupStatusCode(raw.status)) invalidCronBackupStatus();
  const engine = raw.engine;
  if (engine !== "sqlite" && engine !== "postgres" && engine !== "") invalidCronBackupStatus();
  if (engine === "" && raw.status !== "invalid_configuration") invalidCronBackupStatus();
  if (raw.evidence !== "artifact_pair" || raw.time_source !== "mtime" || raw.content_verified !== false) {
    invalidCronBackupStatus();
  }
  const status: CronBackupStatus = {
    status: raw.status,
    engine,
    checkedAt: requiredUtc(raw.checked_at),
    maxAgeSeconds: requiredMaxAgeSeconds(raw.max_age_seconds),
    evidence: "artifact_pair",
    timeSource: "mtime",
    contentVerified: false,
    job: mapJob(raw.job, engine),
  };
  const directory = optionalDirectory(raw);
  if (directory !== undefined) status.directory = directory;
  const latestCompleteAt = optionalUtc(raw, "latest_complete_at");
  if (latestCompleteAt !== undefined) status.latestCompleteAt = latestCompleteAt;
  const artifactName = optionalArtifactName(raw);
  if (artifactName !== undefined) status.artifactName = artifactName;
  return status;
}

export async function getCronBackupStatus(token: string, options?: { signal?: AbortSignal }): Promise<CronBackupStatus> {
  const raw = await request<unknown>("/system/cron-backup-status", { token, signal: options?.signal });
  return mapCronBackupStatus(raw);
}
