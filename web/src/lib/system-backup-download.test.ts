import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";

import type { BackupEntry } from "@/lib/api/system-api";
import { fakeBackupDirectory, fakeBackupLocks } from "./system-backup-download.test-fixtures";
import {
  BACKUP_COMPAT_DOWNLOAD_MAX_BYTES,
  BackupDownloadError,
  prepareBackupDownloadSink,
  saveBackupDownload,
  supportsNativeBackupSave,
  type BackupDownloadSink,
} from "./system-backup-download";

type Bytes = Uint8Array<ArrayBuffer>;
type ReadResult = ReadableStreamReadResult<Bytes>;
type ReadStep = Bytes | "eof" | Error | Promise<ReadResult>;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function makeEntry(overrides: Partial<BackupEntry> = {}): BackupEntry {
  return {
    source: "web",
    engine: "sqlite",
    filename: "xirang-20260101-000000.db",
    size: 4,
    createdAt: "2026-01-01T00:00:00Z",
    sha256: "a".repeat(64),
    downloadable: true,
    unavailableReason: "",
    packagedSize: 6,
    ...overrides,
  };
}

function bytes(...values: number[]): Bytes {
  return new Uint8Array(values);
}

function scriptedResponse(
  steps: ReadStep[],
  options: { status?: number; headers?: Record<string, string>; withoutBody?: boolean; total?: number } = {},
) {
  const reads = [...steps];
  const reader = {
    read: vi.fn((): Promise<ReadResult> => {
      const step = reads.shift() ?? "eof";
      if (step === "eof") {
        return Promise.resolve({ done: true, value: undefined });
      }
      if (step instanceof Error) {
        return Promise.reject(step);
      }
      if (step instanceof Promise) {
        return step;
      }
      return Promise.resolve({ done: false, value: step });
    }),
    cancel: vi.fn(async () => undefined),
    releaseLock: vi.fn(),
  };
  const body = { locked: false, getReader: vi.fn(() => reader), cancel: vi.fn(async () => undefined) };
  const headers = new Headers({
    "content-type": "application/x-tar",
    "content-length": String(options.total ?? 6),
    ...options.headers,
  });
  const response = {
    status: options.status ?? 200,
    headers,
    body: options.withoutBody ? null : body,
  } as unknown as Response;
  return { response, reader, body };
}

type FakeWriter = {
  write: Mock<(chunk: Bytes) => Promise<void>>;
  close: Mock<() => Promise<void>>;
  abort: Mock<() => Promise<void>>;
};

function fakeWriter() {
  const written: number[] = [];
  const writer: FakeWriter = {
    write: vi.fn(async (chunk: Bytes) => {
      written.push(...chunk);
    }),
    close: vi.fn(async () => undefined),
    abort: vi.fn(async () => undefined),
  };
  return { writer, written };
}

const windowProps = ["isSecureContext", "showDirectoryPicker"] as const;
const savedWindowDescriptors: Record<string, PropertyDescriptor | undefined> = {};
const savedUrlDescriptors: Record<string, PropertyDescriptor | undefined> = {};
let savedLocks: PropertyDescriptor | undefined;
let locks = fakeBackupLocks();

function installPicker(picker: (...args: unknown[]) => unknown, secure = true) {
  Object.defineProperty(window, "isSecureContext", { configurable: true, value: secure });
  Object.defineProperty(window, "showDirectoryPicker", { configurable: true, writable: true, value: picker });
}

function pickerReturning(writer: FakeWriter) {
  const createWritable = vi.fn(async () => writer);
  const directory = fakeBackupDirectory(createWritable);
  const picker = vi.fn(async () => directory);
  installPicker(picker);
  return { picker, createWritable, directory };
}

async function nativeSink(entry = makeEntry()) {
  const fake = fakeWriter();
  const { picker, createWritable } = pickerReturning(fake.writer);
  const sink = await prepareBackupDownloadSink(entry, () => true);
  return { sink, picker, createWritable, ...fake };
}

function compatSink(entry = makeEntry()) {
  Reflect.deleteProperty(window, "showDirectoryPicker");
  return prepareBackupDownloadSink(entry, () => true);
}

function saveOptions(overrides: Partial<{ signal: AbortSignal; isCurrent: () => boolean; onProgress: (r: number, t: number) => void }> = {}) {
  return {
    signal: overrides.signal ?? new AbortController().signal,
    isCurrent: overrides.isCurrent ?? (() => true),
    onProgress: overrides.onProgress ?? vi.fn(),
  };
}

async function expectAbort(promise: Promise<unknown>) {
  const error = await promise.then(
    () => null,
    (reason: unknown) => reason,
  );
  expect(error).toBeInstanceOf(DOMException);
  expect((error as DOMException).name).toBe("AbortError");
}

async function expectCode(promise: Promise<unknown>, code: BackupDownloadError["code"]) {
  const error = await promise.then(
    () => null,
    (reason: unknown) => reason,
  );
  expect(error).toBeInstanceOf(BackupDownloadError);
  expect((error as BackupDownloadError).code).toBe(code);
}

let createObjectURL: Mock<(blob: Blob) => string>;
let revokeObjectURL: Mock<(url: string) => void>;
let clicks: { href: string; download: string }[];

beforeEach(() => {
  savedLocks = Object.getOwnPropertyDescriptor(navigator, "locks");
  locks = fakeBackupLocks();
  Object.defineProperty(navigator, "locks", { configurable: true, value: locks });
  for (const prop of windowProps) {
    savedWindowDescriptors[prop] = Object.getOwnPropertyDescriptor(window, prop);
  }
  for (const prop of ["createObjectURL", "revokeObjectURL"]) {
    savedUrlDescriptors[prop] = Object.getOwnPropertyDescriptor(URL, prop);
  }
  createObjectURL = vi.fn<(blob: Blob) => string>(() => "blob:r14-test");
  revokeObjectURL = vi.fn<(url: string) => void>();
  Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: createObjectURL });
  Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: revokeObjectURL });
  clicks = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicks.push({ href: this.getAttribute("href") ?? "", download: this.download });
  });
});

afterEach(() => {
  if (savedLocks) Object.defineProperty(navigator, "locks", savedLocks);
  else Reflect.deleteProperty(navigator, "locks");
  vi.restoreAllMocks();
  vi.useRealTimers();
  for (const [prop, descriptor] of Object.entries(savedWindowDescriptors)) {
    if (descriptor) {
      Object.defineProperty(window, prop, descriptor);
    } else {
      Reflect.deleteProperty(window, prop);
    }
  }
  for (const [prop, descriptor] of Object.entries(savedUrlDescriptors)) {
    if (descriptor) {
      Object.defineProperty(URL, prop, descriptor);
    } else {
      Reflect.deleteProperty(URL, prop);
    }
  }
});

describe("prepareBackupDownloadSink", () => {
  it.each([
    ["nonempty file", "file", [7, 8]],
    ["empty file", "file", []],
    ["directory", "directory", []],
  ] as const)("refuses an existing %s without touching its contents or metadata", async (_label, kind, content) => {
    const { writer } = fakeWriter();
    const { directory, createWritable } = pickerReturning(writer);
    const name = "web-xirang-20260101-000000.db.tar";
    const existing = { kind, bytes: [...content], mtime: 12345 };
    directory.entries.set(name, existing);
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "target_exists");
    expect(directory.entries.get(name)).toEqual({ kind, bytes: [...content], mtime: 12345 });
    expect(createWritable).not.toHaveBeenCalled();
    expect(locks.held.size).toBe(0);
  });

  it("holds one origin-wide lease until abort settles and never queues a competing save", async () => {
    const { writer } = fakeWriter();
    const { directory } = pickerReturning(writer);
    const first = await prepareBackupDownloadSink(makeEntry(), () => true);
    await expectCode(prepareBackupDownloadSink(makeEntry({ filename: "another.db" }), () => true), "save_target_busy");
    const pendingAbort = deferred<void>();
    writer.abort.mockReturnValueOnce(pendingAbort.promise);
    const aborting = first.abort();
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_busy");
    pendingAbort.resolve();
    await aborting;
    expect(directory.entries.size).toBe(1);
    expect(directory.entries.get(first.fileName)?.bytes).toEqual([]);
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "target_exists");
    expect(locks.held.size).toBe(0);
  });

  it("retains the lease over a late create and leaves its new entry without opening a writer", async () => {
    const { writer } = fakeWriter();
    const { directory, createWritable } = pickerReturning(writer);
    const creation = deferred<{ createWritable: typeof createWritable }>();
    directory.getFileHandle.mockImplementationOnce(async () => { throw new DOMException("missing", "NotFoundError"); })
      .mockImplementationOnce(() => creation.promise);
    let current = true;
    const preparing = prepareBackupDownloadSink(makeEntry(), () => current);
    await vi.waitFor(() => expect(directory.getFileHandle).toHaveBeenCalledTimes(2));
    current = false;
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_busy");
    directory.entries.set("web-xirang-20260101-000000.db.tar", { kind: "file", bytes: [], mtime: 1 });
    creation.resolve({ createWritable });
    await expectAbort(preparing);
    expect(createWritable).not.toHaveBeenCalled();
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "target_exists");
  });

  it("waits for a late writer's abort before ending preparation or releasing the lease", async () => {
    const { writer } = fakeWriter();
    const writable = deferred<typeof writer>();
    const cleanup = deferred<void>();
    writer.abort.mockReturnValueOnce(cleanup.promise);
    const createWritable = vi.fn(() => writable.promise);
    installPicker(async () => fakeBackupDirectory(createWritable));
    let current = true;
    const preparing = prepareBackupDownloadSink(makeEntry(), () => current);
    await vi.waitFor(() => expect(createWritable).toHaveBeenCalled());
    current = false;
    writable.resolve(writer);
    await vi.waitFor(() => expect(writer.abort).toHaveBeenCalled());
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_busy");
    cleanup.resolve();
    await expectAbort(preparing);
    expect(locks.held.size).toBe(0);
    expect(writer.close).not.toHaveBeenCalled();
  });

  it.each(["NotAllowedError", "SecurityError", "UnknownError"])("does not create after a %s lookup failure", async (name) => {
    const { writer } = fakeWriter();
    const { directory, createWritable } = pickerReturning(writer);
    directory.getFileHandle.mockRejectedValueOnce(new DOMException("private detail", name));
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_failed");
    expect(directory.entries.size).toBe(0);
    expect(createWritable).not.toHaveBeenCalled();
    expect(locks.held.size).toBe(0);
  });

  it("observes a lock request rejection without starting native writes or compatibility", async () => {
    const { writer } = fakeWriter();
    const { createWritable } = pickerReturning(writer);
    locks.request.mockRejectedValueOnce(new Error("private lock error"));
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_failed");
    expect(createWritable).not.toHaveBeenCalled();
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it.each([undefined, {}])("chooses compatibility before the picker when locks are unusable: %s", async (value) => {
    const picker = vi.fn();
    installPicker(picker);
    Object.defineProperty(navigator, "locks", { configurable: true, value });
    expect(supportsNativeBackupSave()).toBe(false);
    const sink = await prepareBackupDownloadSink(makeEntry(), () => true);
    expect(sink.mode).toBe("compat");
    expect(picker).not.toHaveBeenCalled();
  });

  it("invokes the directory picker synchronously within the caller's activation", () => {
    const order: string[] = [];
    const picker = vi.fn(() => {
      order.push("picker");
      return new Promise(() => undefined);
    });
    installPicker(picker);

    void prepareBackupDownloadSink(makeEntry({ source: "cron", filename: "xirang-sqlite-20260101.db" }), () => true);
    order.push("returned");

    expect(order).toEqual(["picker", "returned"]);
    expect(supportsNativeBackupSave()).toBe(true);
  });

  it.each([
    ["not downloadable", { downloadable: false }],
    ["has unavailable reason", { unavailableReason: "unsafe" as const }],
    ["unknown source", { source: "disk" as unknown as BackupEntry["source"] }],
    ["path traversal filename", { filename: "../xirang.db" }],
    ["slash filename", { filename: "a/b.db" }],
    ["zero packaged size", { packagedSize: 0 }],
    ["fractional packaged size", { packagedSize: 1.5 }],
    ["non-finite packaged size", { packagedSize: Number.NaN }],
  ])("rejects an entry that %s without opening a picker", async (_label, overrides) => {
    const picker = vi.fn();
    installPicker(picker);
    await expectCode(prepareBackupDownloadSink(makeEntry(overrides), () => true), "invalid_entry");
    expect(picker).not.toHaveBeenCalled();
  });

  it("rejects a stale attempt before opening the picker", async () => {
    const picker = vi.fn();
    installPicker(picker);
    await expectAbort(prepareBackupDownloadSink(makeEntry(), () => false));
    expect(picker).not.toHaveBeenCalled();
  });


  it("maps a dismissed picker to cancellation without falling back to memory", async () => {
    installPicker(vi.fn(async () => Promise.reject(new DOMException("dismissed", "AbortError"))));
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "picker_cancelled");
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it.each([
    ["rejects", vi.fn(async () => Promise.reject(new DOMException("denied", "NotAllowedError")))],
    [
      "throws synchronously",
      vi.fn(() => {
        throw new DOMException("no activation", "SecurityError");
      }),
    ],
  ])("reports a picker that %s as a save target failure", async (_label, picker) => {
    installPicker(picker);
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_failed");
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("reports a writer creation failure as a save target failure", async () => {
    const createWritable = vi.fn(async () => Promise.reject(new DOMException("denied", "NotAllowedError")));
    installPicker(vi.fn(async () => fakeBackupDirectory(createWritable)));
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_failed");
  });

  it("drops a handle that arrives after the attempt became stale", async () => {
    const createWritable = vi.fn();
    const directory = fakeBackupDirectory(createWritable);
    const handle = deferred<typeof directory>();
    installPicker(vi.fn(() => handle.promise));
    let current = true;
    const pending = prepareBackupDownloadSink(makeEntry(), () => current);
    current = false;
    handle.resolve(directory);
    await expectAbort(pending);
    expect(createWritable).not.toHaveBeenCalled();
  });

  it("aborts a writer that arrives after the attempt became stale", async () => {
    const { writer } = fakeWriter();
    const writable = deferred<typeof writer>();
    const createWritable = vi.fn(() => writable.promise);
    installPicker(vi.fn(async () => fakeBackupDirectory(createWritable)));
    let current = true;
    const pending = prepareBackupDownloadSink(makeEntry(), () => current);
    await vi.waitFor(() => expect(createWritable).toHaveBeenCalledTimes(1));
    current = false;
    writable.resolve(writer);
    await expectAbort(pending);
    expect(writer.abort).toHaveBeenCalledTimes(1);
    expect(writer.close).not.toHaveBeenCalled();
  });

  it("uses the bounded memory branch outside a secure context", async () => {
    const picker = vi.fn();
    installPicker(picker, false);
    expect(supportsNativeBackupSave()).toBe(false);
    const sink = await prepareBackupDownloadSink(makeEntry(), () => true);
    expect(sink.mode).toBe("compat");
    expect(picker).not.toHaveBeenCalled();
  });

  it("accepts exactly 64 MiB and rejects one byte more before any request", async () => {
    const atLimit = await compatSink(makeEntry({ packagedSize: BACKUP_COMPAT_DOWNLOAD_MAX_BYTES }));
    expect(atLimit.expectedSize).toBe(BACKUP_COMPAT_DOWNLOAD_MAX_BYTES);
    await expectCode(compatSink(makeEntry({ packagedSize: BACKUP_COMPAT_DOWNLOAD_MAX_BYTES + 1 })), "compat_too_large");
  });
});

describe("saveBackupDownload native branch", () => {
  it("streams every byte, reports real progress, and commits after exact EOF", async () => {
    const { sink, writer, written } = await nativeSink();
    const onProgress = vi.fn();
    const { response, reader } = scriptedResponse([bytes(1, 2), bytes(3, 4, 5, 6)]);

    await expect(saveBackupDownload(response, sink, saveOptions({ onProgress }))).resolves.toBe("saved");

    expect(written).toEqual([1, 2, 3, 4, 5, 6]);
    expect(onProgress.mock.calls).toEqual([
      [2, 6],
      [6, 6],
    ]);
    expect(reader.read).toHaveBeenCalledTimes(3);
    expect(writer.close).toHaveBeenCalledTimes(1);
    expect(writer.abort).not.toHaveBeenCalled();
  });

  it.each([
    ["non-200 status", { status: 206 }],
    ["html content type", { headers: { "content-type": "text/html" } }],
    ["json content type", { headers: { "content-type": "application/json" } }],
    ["content encoding", { headers: { "content-encoding": "gzip" } }],
    ["empty content encoding", { headers: { "content-encoding": "" } }],
    ["empty length", { headers: { "content-length": "" } }],
    ["zero length", { headers: { "content-length": "0" } }],
    ["negative length", { headers: { "content-length": "-6" } }],
    ["exponent length", { headers: { "content-length": "6e0" } }],
    ["list length", { headers: { "content-length": "6, 6" } }],
    ["unsafe length", { headers: { "content-length": "9007199254740993" } }],
    ["missing body", { withoutBody: true }],
  ])("rejects a %s before writing", async (_label, options) => {
    const { sink, writer } = await nativeSink();
    const { response, body } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6)], options);

    await expectCode(saveBackupDownload(response, sink, saveOptions()), "invalid_response");

    expect(writer.write).not.toHaveBeenCalled();
    expect(writer.close).not.toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
    if (!("withoutBody" in options)) {
      expect(body.getReader).not.toHaveBeenCalled();
      expect(body.cancel).toHaveBeenCalledTimes(1);
    }
  });

  it("accepts a tar media type with parameters", async () => {
    const { sink } = await nativeSink();
    const { response } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6)], {
      headers: { "content-type": "Application/X-Tar; charset=binary" },
    });
    await expect(saveBackupDownload(response, sink, saveOptions())).resolves.toBe("saved");
  });

  it("requires the response length to match the listed packaged size", async () => {
    const { sink, writer } = await nativeSink();
    const { response } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6, 7)], { total: 7 });
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "selection_changed");
    expect(writer.write).not.toHaveBeenCalled();
  });

  it("aborts on a short body without committing", async () => {
    const { sink, writer } = await nativeSink();
    const { response } = scriptedResponse([bytes(1, 2, 3)]);
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "body_length_mismatch");
    expect(writer.close).not.toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
  });

  it("rejects bytes beyond the declared length before writing them", async () => {
    const { sink, writer, written } = await nativeSink();
    const { response, reader } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6), bytes(7)]);
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "body_length_mismatch");
    expect(written).toEqual([1, 2, 3, 4, 5, 6]);
    expect(writer.close).not.toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
    expect(reader.cancel).toHaveBeenCalled();
  });

  it("rejects an oversized chunk without writing any part of it", async () => {
    const { sink, writer } = await nativeSink();
    const { response } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6, 7)]);
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "body_length_mismatch");
    expect(writer.write).not.toHaveBeenCalled();
  });

  it("aborts when the body reader fails", async () => {
    const { sink, writer } = await nativeSink();
    const { response } = scriptedResponse([bytes(1, 2), new TypeError("network reset")]);
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "transfer_failed");
    expect(writer.close).not.toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
  });

  it("aborts and cancels the body when a disk write fails", async () => {
    const { sink, writer } = await nativeSink();
    writer.write.mockRejectedValueOnce(new DOMException("disk full", "QuotaExceededError"));
    const { response, reader } = scriptedResponse([bytes(1, 2), bytes(3, 4, 5, 6)]);
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "write_failed");
    expect(writer.close).not.toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
    expect(reader.cancel).toHaveBeenCalled();
    expect(reader.read).toHaveBeenCalledTimes(1);
  });

  it("reports a failed close without aborting or truncating the chosen file", async () => {
    const { sink, writer } = await nativeSink();
    writer.close.mockRejectedValueOnce(new DOMException("rename failed", "InvalidStateError"));
    const { response } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6)]);
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "commit_failed");
    expect(writer.abort).not.toHaveBeenCalled();
  });

  it("stops before reading when already stale", async () => {
    const { sink, writer } = await nativeSink();
    const { response, body } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6)]);
    await expectAbort(saveBackupDownload(response, sink, saveOptions({ isCurrent: () => false })));
    expect(body.getReader).not.toHaveBeenCalled();
    expect(body.cancel).toHaveBeenCalledTimes(1);
    expect(writer.abort).toHaveBeenCalledTimes(1);
  });

  it("discards a read that resolves after an A to B to A identity transition", async () => {
    const { sink, writer } = await nativeSink();
    const scope = { user: "A", generation: 1 };
    const captured = { ...scope };
    const isCurrent = () => scope.user === captured.user && scope.generation === captured.generation;
    const read = deferred<ReadResult>();
    const { response, reader } = scriptedResponse([read.promise]);

    const pending = saveBackupDownload(response, sink, saveOptions({ isCurrent }));
    await vi.waitFor(() => expect(reader.read).toHaveBeenCalledTimes(1));
    Object.assign(scope, { user: "B", generation: 2 });
    Object.assign(scope, { user: "A", generation: 3 });
    read.resolve({ done: false, value: bytes(1, 2, 3, 4, 5, 6) });

    await expectAbort(pending);
    expect(writer.write).not.toHaveBeenCalled();
    expect(writer.close).not.toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
  });

  it("stops after a write that finished once the attempt became stale", async () => {
    const { sink, writer } = await nativeSink();
    let current = true;
    const write = deferred<void>();
    writer.write.mockImplementationOnce(() => write.promise);
    const onProgress = vi.fn();
    const { response, reader } = scriptedResponse([bytes(1, 2), bytes(3, 4, 5, 6)]);

    const pending = saveBackupDownload(response, sink, saveOptions({ isCurrent: () => current, onProgress }));
    await vi.waitFor(() => expect(writer.write).toHaveBeenCalledTimes(1));
    current = false;
    write.resolve();

    await expectAbort(pending);
    expect(onProgress).not.toHaveBeenCalled();
    expect(reader.read).toHaveBeenCalledTimes(1);
    expect(writer.close).not.toHaveBeenCalled();
  });

  it("stops reading when progress reporting observes a stale attempt", async () => {
    const { sink, writer } = await nativeSink();
    let current = true;
    const { response, reader } = scriptedResponse([bytes(1, 2), bytes(3, 4, 5, 6)]);
    const onProgress = vi.fn(() => {
      current = false;
    });
    await expectAbort(saveBackupDownload(response, sink, saveOptions({ isCurrent: () => current, onProgress })));
    expect(reader.read).toHaveBeenCalledTimes(1);
    expect(writer.close).not.toHaveBeenCalled();
  });

  it("does not commit when identity changes at EOF", async () => {
    const { sink, writer } = await nativeSink();
    let current = true;
    const eof = deferred<ReadResult>();
    const { response } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6), eof.promise]);

    const pending = saveBackupDownload(response, sink, saveOptions({ isCurrent: () => current }));
    await vi.waitFor(() => expect(writer.write).toHaveBeenCalledTimes(1));
    current = false;
    eof.resolve({ done: true, value: undefined });

    await expectAbort(pending);
    expect(writer.close).not.toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
  });

  it("lets a started close settle without truncating when identity changes during commit", async () => {
    const { sink, writer } = await nativeSink();
    const close = deferred<void>();
    writer.close.mockImplementationOnce(() => close.promise);
    let current = true;
    const controller = new AbortController();
    const { response } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6)]);

    const pending = saveBackupDownload(response, sink, saveOptions({ isCurrent: () => current, signal: controller.signal }));
    await vi.waitFor(() => expect(writer.close).toHaveBeenCalledTimes(1));
    current = false;
    controller.abort();
    const abortSettled = vi.fn();
    void sink.abort().then(abortSettled);
    await Promise.resolve();
    expect(abortSettled).not.toHaveBeenCalled();
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_busy");
    close.resolve();

    await expectAbort(pending);
    expect(abortSettled).toHaveBeenCalled();
    expect(writer.abort).not.toHaveBeenCalled();
    expect(locks.held.size).toBe(0);
  });

  it("cancels an in-flight read and the writer when the signal aborts", async () => {
    const { sink, writer } = await nativeSink();
    const controller = new AbortController();
    const read = deferred<ReadResult>();
    const { response, reader } = scriptedResponse([read.promise]);

    const pending = saveBackupDownload(response, sink, saveOptions({ signal: controller.signal }));
    await vi.waitFor(() => expect(reader.read).toHaveBeenCalledTimes(1));
    controller.abort();
    expect(reader.cancel).toHaveBeenCalled();
    expect(writer.abort).toHaveBeenCalledTimes(1);
    read.reject(new TypeError("cancelled"));

    await expectAbort(pending);
    expect(writer.write).not.toHaveBeenCalled();
    expect(writer.close).not.toHaveBeenCalled();
  });

  it("reports a write rejected by a concurrent abort as cancellation", async () => {
    const { sink, writer } = await nativeSink();
    const controller = new AbortController();
    const write = deferred<void>();
    writer.write.mockImplementationOnce(() => write.promise);
    const { response } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6)]);

    const pending = saveBackupDownload(response, sink, saveOptions({ signal: controller.signal }));
    await vi.waitFor(() => expect(writer.write).toHaveBeenCalledTimes(1));
    controller.abort();
    await expectCode(prepareBackupDownloadSink(makeEntry(), () => true), "save_target_busy");
    write.reject(new DOMException("stream aborted", "AbortError"));

    await expectAbort(pending);
    expect(writer.abort).toHaveBeenCalledTimes(1);
    expect(writer.close).not.toHaveBeenCalled();
    expect(locks.held.size).toBe(0);
  });

  it("keeps sink abort idempotent and rejects later use", async () => {
    const { sink, writer } = await nativeSink();
    await Promise.all([sink.abort(), sink.abort()]);
    expect(writer.abort).toHaveBeenCalledTimes(1);
    await expectAbort(sink.write(bytes(1)));
    await expectAbort(sink.commit());
  });
});

describe("saveBackupDownload compatibility branch", () => {
  it("hands an exact Blob to the browser download and revokes the URL", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    const sink = await compatSink();
    const { response } = scriptedResponse([bytes(1, 2, 3), bytes(4, 5, 6)]);

    await expect(saveBackupDownload(response, sink, saveOptions())).resolves.toBe("download_started");

    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const blob = createObjectURL.mock.calls[0][0] as Blob;
    expect(blob.size).toBe(6);
    expect(blob.type).toBe("application/x-tar");
    expect(clicks).toEqual([{ href: "blob:r14-test", download: "web-xirang-20260101-000000.db.tar" }]);
    expect(document.querySelector("a[download]")).toBeNull();
    expect(revokeObjectURL).not.toHaveBeenCalled();
    vi.runAllTimers();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:r14-test");
  });

  it("saves exactly 64 MiB", async () => {
    const sink = await compatSink(makeEntry({ packagedSize: BACKUP_COMPAT_DOWNLOAD_MAX_BYTES }));
    const { response } = scriptedResponse([new Uint8Array(BACKUP_COMPAT_DOWNLOAD_MAX_BYTES)], {
      total: BACKUP_COMPAT_DOWNLOAD_MAX_BYTES,
    });
    await expect(saveBackupDownload(response, sink, saveOptions())).resolves.toBe("download_started");
    expect((createObjectURL.mock.calls[0][0] as Blob).size).toBe(BACKUP_COMPAT_DOWNLOAD_MAX_BYTES);
  });

  it("refuses a response above 64 MiB before reading it", async () => {
    const sink = await compatSink();
    const { response, body } = scriptedResponse([], { total: BACKUP_COMPAT_DOWNLOAD_MAX_BYTES + 1 });
    await expectCode(saveBackupDownload(response, sink, saveOptions()), "compat_too_large");
    expect(body.getReader).not.toHaveBeenCalled();
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("creates no download when identity changes at EOF", async () => {
    const sink = await compatSink();
    let current = true;
    const eof = deferred<ReadResult>();
    const { response, reader } = scriptedResponse([bytes(1, 2, 3, 4, 5, 6), eof.promise]);

    const pending = saveBackupDownload(response, sink, saveOptions({ isCurrent: () => current }));
    await vi.waitFor(() => expect(reader.read).toHaveBeenCalledTimes(2));
    current = false;
    eof.resolve({ done: true, value: undefined });

    await expectAbort(pending);
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(clicks).toEqual([]);
  });

  it("creates no download after a short body or reader failure", async () => {
    const shortSink = await compatSink();
    await expectCode(
      saveBackupDownload(scriptedResponse([bytes(1, 2)]).response, shortSink, saveOptions()),
      "body_length_mismatch",
    );
    const failingSink = await compatSink();
    await expectCode(
      saveBackupDownload(scriptedResponse([bytes(1, 2), new Error("reset")]).response, failingSink, saveOptions()),
      "transfer_failed",
    );
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(clicks).toEqual([]);
  });

  it("rejects writes and commits after abort", async () => {
    const sink: BackupDownloadSink = await compatSink();
    await sink.write(bytes(1, 2, 3, 4, 5, 6));
    await sink.abort();
    await expectAbort(sink.write(bytes(1)));
    await expectAbort(sink.commit());
    expect(createObjectURL).not.toHaveBeenCalled();
  });
});
