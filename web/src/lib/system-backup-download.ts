import type { BackupEntry } from "@/lib/api/system-api";

/** Payload bound for the in-memory compatibility branch (not a peak-memory guarantee). */
export const BACKUP_COMPAT_DOWNLOAD_MAX_BYTES = 64 * 1024 * 1024;

const TAR_MEDIA_TYPE = "application/x-tar";
const MANAGED_FILENAME_PATTERN = /^[A-Za-z0-9_-][A-Za-z0-9._-]*$/;
// The anchor download reads the Blob asynchronously after click(); revoking the URL in the same task
// cancels the download in some engines, so release it on a short delay instead.
const OBJECT_URL_REVOKE_DELAY_MS = 10_000;

export type BackupDownloadResult = "saved" | "download_started";

export type BackupDownloadSinkMode = "native" | "compat";

/**
 * Fixed, non-sensitive failure categories. Messages are developer-facing; UI must map `code` to localized text.
 * Identity/attempt invalidation and signal cancellation always reject with a DOMException named "AbortError".
 */
export type BackupDownloadErrorCode =
  | "invalid_entry"
  | "compat_too_large"
  | "picker_cancelled"
  | "save_target_failed"
  | "target_exists"
  | "save_target_busy"
  | "invalid_response"
  | "selection_changed"
  | "transfer_failed"
  | "body_length_mismatch"
  | "write_failed"
  | "commit_failed";

const ERROR_MESSAGES: Record<BackupDownloadErrorCode, string> = {
  invalid_entry: "Backup entry is not downloadable",
  compat_too_large: "Backup exceeds the browser compatibility download limit",
  picker_cancelled: "Save location selection was cancelled",
  save_target_failed: "Save location could not be opened",
  target_exists: "The destination already contains this name",
  save_target_busy: "Another native backup save is still active",
  invalid_response: "Backup download response is invalid",
  selection_changed: "Backup changed since it was listed",
  transfer_failed: "Backup transfer failed",
  body_length_mismatch: "Backup transfer length does not match",
  write_failed: "Backup could not be written",
  commit_failed: "Backup could not be finalized",
};

export class BackupDownloadError extends Error {
  readonly code: BackupDownloadErrorCode;

  constructor(code: BackupDownloadErrorCode) {
    super(ERROR_MESSAGES[code]);
    this.name = "BackupDownloadError";
    this.code = code;
  }
}

export type BackupDownloadSink = {
  readonly mode: BackupDownloadSinkMode;
  readonly fileName: string;
  /** Exact tar byte count selected from the list (`packagedSize`); the response must match it. */
  readonly expectedSize: number;
  write(chunk: Uint8Array<ArrayBuffer>): Promise<void>;
  commit(): Promise<BackupDownloadResult>;
  /** Idempotent. Before commit discards pending data; once commit started it only waits for it to settle. */
  abort(): Promise<void>;
};

export type SaveBackupDownloadOptions = {
  signal: AbortSignal;
  isCurrent: () => boolean;
  onProgress: (received: number, total: number) => void;
};

// Minimal local File System Access shapes; intentionally not global declarations.
type DirectoryPickerOptions = { mode: "readwrite" };
const NATIVE_SAVE_LOCK = "xirang:system-backup-download:native-save";

interface BackupWritableFileStream {
  write(data: Uint8Array<ArrayBuffer>): Promise<void>;
  close(): Promise<void>;
  abort(reason?: unknown): Promise<void>;
}

interface BackupFileHandle {
  createWritable(options: { keepExistingData: false; mode: "exclusive" }): Promise<BackupWritableFileStream>;
}

interface BackupDirectoryHandle {
  getFileHandle(name: string, options: { create: boolean }): Promise<BackupFileHandle>;
}

type DirectoryPickerHost = {
  isSecureContext?: boolean;
  showDirectoryPicker?: (options: DirectoryPickerOptions) => Promise<BackupDirectoryHandle>;
};

function pickerHost(): (DirectoryPickerHost & { locks: LockManager }) | null {
  if (typeof window === "undefined" || typeof navigator === "undefined") return null;
  const host: DirectoryPickerHost = window;
  if (host.isSecureContext !== true || typeof host.showDirectoryPicker !== "function"
    || typeof navigator.locks?.request !== "function") return null;
  return { showDirectoryPicker: host.showDirectoryPicker.bind(window), locks: navigator.locks };
}

/** True only when directory selection and same-origin cooperative locking are both available. */
export function supportsNativeBackupSave(): boolean {
  return pickerHost() !== null;
}

export function backupDownloadFileName(entry: Pick<BackupEntry, "source" | "filename">): string {
  return `${entry.source}-${entry.filename}.tar`;
}

function abortError(): DOMException {
  return new DOMException("Backup download aborted", "AbortError");
}

/**
 * Must be called synchronously from the confirming user gesture, before any await, so the
 * directory picker still holds transient user activation. Native failures never fall back to memory.
 */
export function prepareBackupDownloadSink(entry: BackupEntry, isCurrent: () => boolean): Promise<BackupDownloadSink> {
  const downloadable =
    entry.downloadable === true &&
    entry.unavailableReason === "" &&
    (entry.source === "web" || entry.source === "cron") &&
    MANAGED_FILENAME_PATTERN.test(entry.filename) &&
    Number.isSafeInteger(entry.packagedSize) &&
    entry.packagedSize > 0;
  if (!downloadable) {
    return Promise.reject(new BackupDownloadError("invalid_entry"));
  }
  if (!isCurrent()) {
    return Promise.reject(abortError());
  }
  const fileName = backupDownloadFileName(entry);
  const expectedSize = entry.packagedSize;
  const host = pickerHost();
  if (!host?.showDirectoryPicker) {
    if (expectedSize > BACKUP_COMPAT_DOWNLOAD_MAX_BYTES) {
      return Promise.reject(new BackupDownloadError("compat_too_large"));
    }
    return Promise.resolve(createCompatSink(fileName, expectedSize));
  }
  let picked: Promise<BackupDirectoryHandle>;
  try {
    // Invoked synchronously within the caller's gesture, before requesting authorization.
    picked = host.showDirectoryPicker({ mode: "readwrite" });
  } catch (error) {
    picked = Promise.reject(error);
  }
  return openNativeSink(picked, host.locks, fileName, expectedSize, isCurrent);
}

async function openNativeSink(
  picked: Promise<BackupDirectoryHandle>,
  locks: LockManager,
  fileName: string,
  expectedSize: number,
  isCurrent: () => boolean,
): Promise<BackupDownloadSink> {
  let directory: BackupDirectoryHandle;
  try {
    directory = await picked;
  } catch (error) {
    if (!isCurrent()) throw abortError();
    const dismissed = error instanceof DOMException && error.name === "AbortError";
    throw new BackupDownloadError(dismissed ? "picker_cancelled" : "save_target_failed");
  }
  if (!isCurrent()) throw abortError();

  // Readiness publishes the sink; terminal keeps the callback (and lock) alive until cleanup.
  let ready!: (sink: BackupDownloadSink) => void;
  let failed!: (error: unknown) => void;
  const preparation = new Promise<BackupDownloadSink>((resolve, reject) => { ready = resolve; failed = reject; });
  let terminal!: () => void;
  const terminated = new Promise<void>((resolve) => { terminal = resolve; });
  let released!: () => void;
  const leaseReleased = new Promise<void>((resolve) => { released = resolve; });
  const checkCurrent = () => { if (!isCurrent()) throw abortError(); };
  try {
    const request = locks.request(NATIVE_SAVE_LOCK, { mode: "exclusive", ifAvailable: true }, async (lock) => {
      let writer: BackupWritableFileStream | null = null;
      try {
        if (!lock) throw new BackupDownloadError("save_target_busy");
        checkCurrent();
        try {
          await directory.getFileHandle(fileName, { create: false });
          checkCurrent();
          throw new BackupDownloadError("target_exists");
        } catch (error) {
          checkCurrent();
          if (error instanceof DOMException && error.name === "TypeMismatchError") {
            throw new BackupDownloadError("target_exists");
          }
          if (!(error instanceof DOMException && error.name === "NotFoundError")) throw error;
        }
        const handle = await directory.getFileHandle(fileName, { create: true });
        checkCurrent();
        writer = await handle.createWritable({ keepExistingData: false, mode: "exclusive" });
        checkCurrent();
        ready(createNativeSink(writer, fileName, expectedSize, terminal, leaseReleased));
        await terminated;
      } catch (error) {
        // Catch late resources before invalidation: never release a lease over a pending native operation.
        if (writer) await writer.abort().catch(() => undefined);
        failed(!isCurrent() ? abortError() : error instanceof BackupDownloadError ? error : new BackupDownloadError("save_target_failed"));
      }
    });
    // Observe rejection even after readiness; this promise is separate from the callback's terminal wait.
    void request.then(released, (error: unknown) => {
      failed(!isCurrent() ? abortError() : new BackupDownloadError("save_target_failed"));
      released();
      void error;
    });
  } catch {
    failed(!isCurrent() ? abortError() : new BackupDownloadError("save_target_failed"));
    released();
  }
  try {
    return await preparation;
  } catch (error) {
    await leaseReleased;
    throw error;
  }
}

function createNativeSink(
  writer: BackupWritableFileStream,
  fileName: string,
  expectedSize: number,
  terminal: () => void,
  leaseReleased: Promise<void>,
): BackupDownloadSink {
  let state: "open" | "closing" | "closed" | "aborted" = "open";
  let writePromise: Promise<void> | null = null;
  let closePromise: Promise<BackupDownloadResult> | null = null;
  let abortPromise: Promise<void> | null = null;
  const finish = async () => {
    terminal();
    await leaseReleased;
  };
  return {
    mode: "native",
    fileName,
    expectedSize,
    async write(chunk) {
      if (state !== "open") throw abortError();
      writePromise = (async () => { await writer.write(chunk); })();
      try {
        await writePromise;
      } catch {
        throw state === "open" ? new BackupDownloadError("write_failed") : abortError();
      } finally {
        writePromise = null;
      }
      if (state !== "open") throw abortError();
    },
    commit() {
      if (closePromise) return closePromise;
      if (state !== "open") return Promise.reject(abortError());
      // The caller has validated EOF and awaited every write. close starts the irreversible boundary.
      state = "closing";
      closePromise = (async () => {
        try {
          await writer.close();
          return "saved" as const;
        } catch {
          throw new BackupDownloadError("commit_failed");
        } finally {
          state = "closed";
          await finish();
        }
      })();
      return closePromise;
    },
    abort() {
      if (closePromise) return closePromise.then(() => undefined, () => undefined);
      if (abortPromise) return abortPromise;
      state = "aborted";
      const pendingWrite = writePromise;
      abortPromise = (async () => {
        await Promise.allSettled([pendingWrite, (async () => { await writer.abort(); })()]);
        await finish();
      })();
      return abortPromise;
    },
  };
}

function createCompatSink(fileName: string, expectedSize: number): BackupDownloadSink {
  let state: "open" | "committed" | "aborted" = "open";
  let chunks: Uint8Array<ArrayBuffer>[] = [];
  let received = 0;

  return {
    mode: "compat",
    fileName,
    expectedSize,
    async write(chunk) {
      if (state !== "open") {
        throw abortError();
      }
      if (chunk.byteLength > expectedSize - received || received + chunk.byteLength > BACKUP_COMPAT_DOWNLOAD_MAX_BYTES) {
        throw new BackupDownloadError("body_length_mismatch");
      }
      chunks.push(chunk);
      received += chunk.byteLength;
    },
    async commit() {
      if (state !== "open") {
        throw abortError();
      }
      if (received !== expectedSize) {
        throw new BackupDownloadError("body_length_mismatch");
      }
      let url: string | null = null;
      try {
        const blob = new Blob(chunks, { type: TAR_MEDIA_TYPE });
        chunks = [];
        url = URL.createObjectURL(blob);
        const anchor = document.createElement("a");
        anchor.href = url;
        anchor.download = fileName;
        anchor.rel = "noopener";
        anchor.style.display = "none";
        document.body.appendChild(anchor);
        state = "committed";
        try {
          anchor.click();
        } finally {
          anchor.remove();
        }
      } catch {
        chunks = [];
        if (url !== null) {
          URL.revokeObjectURL(url);
        }
        if (state === "open") {
          state = "aborted";
        }
        throw new BackupDownloadError("commit_failed");
      }
      const committedUrl = url;
      setTimeout(() => URL.revokeObjectURL(committedUrl), OBJECT_URL_REVOKE_DELAY_MS);
      return "download_started";
    },
    async abort() {
      if (state === "open") {
        state = "aborted";
      }
      chunks = [];
    },
  };
}

function parseContentLength(value: string | null): number | null {
  if (value === null || !/^[0-9]+$/.test(value)) {
    return null;
  }
  const length = Number(value);
  return Number.isSafeInteger(length) && length > 0 ? length : null;
}

function validateResponse(response: Response, sink: BackupDownloadSink): number {
  if (response.status !== 200 || response.body === null) {
    throw new BackupDownloadError("invalid_response");
  }
  const mediaType = (response.headers.get("content-type") ?? "").split(";")[0].trim().toLowerCase();
  if (mediaType !== TAR_MEDIA_TYPE) {
    throw new BackupDownloadError("invalid_response");
  }
  const encoding = response.headers.get("content-encoding");
  if (encoding !== null) {
    throw new BackupDownloadError("invalid_response");
  }
  const total = parseContentLength(response.headers.get("content-length"));
  if (total === null) {
    throw new BackupDownloadError("invalid_response");
  }
  if (sink.mode === "compat" && total > BACKUP_COMPAT_DOWNLOAD_MAX_BYTES) {
    throw new BackupDownloadError("compat_too_large");
  }
  if (total !== sink.expectedSize) {
    throw new BackupDownloadError("selection_changed");
  }
  return total;
}

/**
 * Streams an already-validated tar response into the prepared sink. Bytes are committed only after an exact EOF
 * and a final ownership check. Stale identity or signal abort rejects with AbortError; once the native close or
 * compatibility anchor click has started the save is irrevocable and is only awaited, never truncated.
 */
export async function saveBackupDownload(
  response: Response,
  sink: BackupDownloadSink,
  options: SaveBackupDownloadOptions,
): Promise<BackupDownloadResult> {
  const { signal, isCurrent, onProgress } = options;
  const stale = () => signal.aborted || !isCurrent();
  const assertCurrent = () => {
    if (stale()) {
      throw abortError();
    }
  };
  let reader: ReadableStreamDefaultReader<Uint8Array<ArrayBuffer>> | null = null;
  let commitStarted = false;
  const onAbort = () => {
    void reader?.cancel().catch(() => undefined);
    if (!commitStarted) {
      void sink.abort();
    }
  };
  signal.addEventListener("abort", onAbort, { once: true });

  try {
    assertCurrent();
    const total = validateResponse(response, sink);
    reader = (response.body as ReadableStream<Uint8Array<ArrayBuffer>>).getReader();
    let received = 0;
    for (;;) {
      assertCurrent();
      let chunk: ReadableStreamReadResult<Uint8Array<ArrayBuffer>>;
      try {
        chunk = await reader.read();
      } catch {
        assertCurrent();
        throw new BackupDownloadError("transfer_failed");
      }
      assertCurrent();
      if (chunk.done) {
        break;
      }
      const value = chunk.value;
      if (value.byteLength > total - received) {
        throw new BackupDownloadError("body_length_mismatch");
      }
      if (value.byteLength === 0) {
        continue;
      }
      await sink.write(value);
      assertCurrent();
      received += value.byteLength;
      onProgress(received, total);
      assertCurrent();
    }
    if (received !== total) {
      throw new BackupDownloadError("body_length_mismatch");
    }
    assertCurrent();
    commitStarted = true;
    const result = await sink.commit();
    assertCurrent();
    return result;
  } catch (error) {
    await sink.abort();
    if (reader !== null) {
      await reader.cancel().catch(() => undefined);
    } else if (response.body !== null && !response.body.locked) {
      await response.body.cancel().catch(() => undefined);
    }
    throw stale() ? abortError() : error;
  } finally {
    signal.removeEventListener("abort", onAbort);
    try {
      reader?.releaseLock();
    } catch {
      // A pending read on a cancelled stream may keep the lock briefly; cancellation already released resources.
    }
  }
}
