import { request, requestStream } from "./core";
import { finiteNumber } from "./number-utils";
import type { CronBackupStatus } from "./cron-backup-status";

export type VersionInfo = {
  version: string;
  buildTime: string;
  gitCommit: string;
};

export type VersionCheck = {
  updateAvailable: boolean;
  currentVersion: string;
  latestVersion: string;
  releaseUrl: string;
};

export type BackupResult = {
  filename: string;
  path: string;
  size: number;
  sha256: string;
};

export type BackupSource = "web" | "cron";
export type BackupEngine = "sqlite" | "postgres";
export type BackupUnavailableReason = "" | "unmanaged" | "incomplete" | "unsafe" | "too_large";

/**
 * One row of a single backup source. `downloadable` only means a download may be attempted; it does not
 * attest content integrity or retention. It is true only when every identity field validated.
 */
export type BackupEntry = {
  source: BackupSource;
  engine: BackupEngine;
  filename: string;
  size: number;
  createdAt: string;
  sha256: string;
  downloadable: boolean;
  unavailableReason: BackupUnavailableReason;
  packagedSize: number;
};

type RawVersionInfo = {
  version?: unknown;
  build_time?: unknown;
  git_commit?: unknown;
};

type RawVersionCheck = {
  update_available?: unknown;
  current_version?: unknown;
  latest_version?: unknown;
  release_url?: unknown;
};

type RawBackupResult = {
  filename?: unknown;
  path?: unknown;
  size?: unknown;
  sha256?: unknown;
};

type RawBackupEntry = {
  source?: unknown;
  engine?: unknown;
  filename?: unknown;
  size?: unknown;
  created_at?: unknown;
  sha256?: unknown;
  downloadable?: unknown;
  unavailable_reason?: unknown;
  packaged_size?: unknown;
};

const BACKUP_DIGEST_PATTERN = /^[0-9a-f]{64}$/;
// Managed web/cron names only use this charset; anything else is never offered for download.
const DOWNLOADABLE_FILENAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$/;

function invalidBackupList(): Error {
  return new Error("invalid backup list");
}

function isNonNegativeSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

export function mapVersionInfo(row: RawVersionInfo | null | undefined): VersionInfo {
  return {
    version: String(row?.version ?? ""),
    buildTime: String(row?.build_time ?? ""),
    gitCommit: String(row?.git_commit ?? ""),
  };
}

export function mapVersionCheck(row: RawVersionCheck | null | undefined): VersionCheck {
  return {
    updateAvailable: Boolean(row?.update_available),
    currentVersion: String(row?.current_version ?? ""),
    latestVersion: String(row?.latest_version ?? ""),
    releaseUrl: String(row?.release_url ?? ""),
  };
}

export function mapBackupResult(row: RawBackupResult | null | undefined): BackupResult {
  return {
    filename: String(row?.filename ?? ""),
    path: String(row?.path ?? ""),
    size: finiteNumber(row?.size),
    sha256: String(row?.sha256 ?? ""),
  };
}

/**
 * Maps one list row of `expectedSource`. Structural identity (source, engine, filename, size, time) must be
 * valid or the whole list is rejected; download eligibility fails closed to an unavailable reason.
 */
export function mapBackupEntry(row: RawBackupEntry | null | undefined, expectedSource: BackupSource): BackupEntry {
  if (!row || typeof row !== "object") {
    throw invalidBackupList();
  }
  const { source, engine, filename, size, created_at: createdAt } = row;
  if ((source !== "web" && source !== "cron") || source !== expectedSource) {
    throw invalidBackupList();
  }
  if (engine !== "sqlite" && engine !== "postgres") {
    throw invalidBackupList();
  }
  if (source === "web" && engine !== "sqlite") {
    throw invalidBackupList();
  }
  if (typeof filename !== "string" || filename === "" || filename.includes("/") || filename.includes("\0")) {
    throw invalidBackupList();
  }
  if (!isNonNegativeSafeInteger(size) || typeof createdAt !== "string") {
    throw invalidBackupList();
  }

  const sha256 = typeof row.sha256 === "string" && BACKUP_DIGEST_PATTERN.test(row.sha256) ? row.sha256 : "";
  const packagedSize = isNonNegativeSafeInteger(row.packaged_size) ? row.packaged_size : 0;
  const rawReason = row.unavailable_reason;
  // Unknown or missing reasons fail closed as "unsafe" and never imply availability.
  const reason: BackupUnavailableReason = rawReason === "" || rawReason === "unmanaged" || rawReason === "incomplete" || rawReason === "too_large"
    ? rawReason
    : "unsafe";
  const downloadable = row.downloadable === true
    && reason === ""
    && sha256 !== ""
    && size > 0
    && packagedSize > 0
    && DOWNLOADABLE_FILENAME_PATTERN.test(filename);
  return {
    source,
    engine,
    filename,
    size,
    createdAt,
    sha256,
    downloadable,
    // A row that is not downloadable always carries a visible reason, never "".
    unavailableReason: downloadable ? "" : reason === "" ? "unsafe" : reason,
    packagedSize,
  };
}

export function createSystemApi() {
  return {
    async getVersion(options?: { signal?: AbortSignal }): Promise<VersionInfo> {
      const raw = await request<RawVersionInfo>("/version", { signal: options?.signal });
      return mapVersionInfo(raw);
    },

    async checkVersion(token: string, options?: { signal?: AbortSignal }): Promise<VersionCheck> {
      const raw = await request<RawVersionCheck>("/version/check", { token, signal: options?.signal });
      return mapVersionCheck(raw);
    },

    async backupDB(token: string, options?: { signal?: AbortSignal }): Promise<BackupResult> {
      const raw = await request<RawBackupResult>("/system/backup-db", {
        token,
        method: "POST",
        signal: options?.signal,
      });
      return mapBackupResult(raw);
    },

    async listBackups(token: string, options?: { signal?: AbortSignal; source?: BackupSource }): Promise<BackupEntry[]> {
      const source = options?.source ?? "web";
      const path = source === "cron" ? "/system/backups?source=cron" : "/system/backups";
      const raw = await request<unknown>(path, { token, signal: options?.signal });
      if (!Array.isArray(raw)) {
        throw invalidBackupList();
      }
      return raw.map((row: RawBackupEntry | null | undefined) => mapBackupEntry(row, source));
    },

    /**
     * Starts the tar download of one listed entry. Ownership is checked before the request and again when
     * headers arrive: a stale owner gets an AbortError and the unread body is cancelled. The returned body
     * is unread; the caller owns consuming or cancelling it.
     */
    async downloadBackup(
      token: string,
      entry: Pick<BackupEntry, "source" | "filename" | "sha256">,
      stepUpProof: string,
      options: { signal: AbortSignal; isCurrent: () => boolean },
    ): Promise<Response> {
      if (options.signal.aborted || !options.isCurrent()) {
        throw new DOMException("Aborted", "AbortError");
      }
      if ((entry.source !== "web" && entry.source !== "cron")
        || !DOWNLOADABLE_FILENAME_PATTERN.test(entry.filename)
        || !BACKUP_DIGEST_PATTERN.test(entry.sha256)) {
        throw new Error("invalid backup download target");
      }
      let response: Response;
      try {
        response = await requestStream(
          `/system/backups/${encodeURIComponent(entry.source)}/${encodeURIComponent(entry.filename)}/download`,
          { token, stepUpProof, signal: options.signal, ifMatch: `"${entry.sha256}"` },
        );
      } catch (error) {
        if (options.signal.aborted || !options.isCurrent()) {
          throw new DOMException("Aborted", "AbortError");
        }
        throw error;
      }
      if (options.signal.aborted || !options.isCurrent()) {
        void response.body?.cancel().catch(() => undefined);
        throw new DOMException("Aborted", "AbortError");
      }
      return response;
    },

    async getCronBackupStatus(token: string, options?: { signal?: AbortSignal }): Promise<CronBackupStatus> {
      // Static import would put the cron mapper in the eagerly loaded client chunk.
      const cronBackupStatus = await import("./cron-backup-status");
      return cronBackupStatus.getCronBackupStatus(token, options);
    },
  };
}
