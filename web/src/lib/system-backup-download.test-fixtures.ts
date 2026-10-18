import { vi } from "vitest";

/** Models namespace admission; bytes/mtime are deliberately untouched by lookup and abort. */
export function fakeBackupDirectory<T>(createWritable: () => Promise<T>) {
  const entries = new Map<string, { kind: "file" | "directory"; bytes: number[]; mtime: number }>();
  const getFileHandle = vi.fn(async (name: string, options: { create: boolean }) => {
    const existing = entries.get(name);
    if (existing?.kind === "directory") throw new DOMException("directory", "TypeMismatchError");
    if (!existing && !options.create) throw new DOMException("missing", "NotFoundError");
    if (!existing) entries.set(name, { kind: "file", bytes: [], mtime: 1 });
    return { createWritable };
  });
  return { entries, getFileHandle };
}

/** Holds the lock until the actual callback promise settles, including asynchronous cleanup. */
export function fakeBackupLocks() {
  const held = new Set<string>();
  const request = vi.fn(async <T>(name: string, options: { mode: string; ifAvailable: boolean }, callback: (lock: { name: string; mode: string } | null) => Promise<T>) => {
    if (options.mode !== "exclusive" || !options.ifAvailable) throw new Error("Unexpected lock protocol");
    if (held.has(name)) return await callback(null);
    held.add(name);
    try {
      return await callback({ name, mode: "exclusive" });
    } finally {
      held.delete(name);
    }
  });
  return { request, held };
}
