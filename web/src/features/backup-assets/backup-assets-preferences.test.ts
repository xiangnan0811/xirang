import { describe, expect, it, vi } from "vitest";

import {
  BACKUP_ASSETS_PREFERENCES_KEY,
  DEFAULT_BACKUP_ASSETS_PREFERENCES,
  decodeBackupAssetsPreferences,
  readBackupAssetsPreferences,
  resolveBackupAssetsLayout,
  writeBackupAssetsPreferences,
} from "./backup-assets-preferences";

describe("backup assets preferences", () => {
  it("decodes the exact bounded v1 schema", () => {
    expect(
      decodeBackupAssetsPreferences(
        JSON.stringify({ version: 1, layout: "grid", contextWidth: 288, inspectorWidth: 416 })
      )
    ).toEqual({ version: 1, layout: "grid", contextWidth: 288, inspectorWidth: 416 });
  });

  it.each([
    null,
    "",
    "not-json",
    JSON.stringify({ version: 2, layout: "list", contextWidth: 288, inspectorWidth: 416 }),
    JSON.stringify({ version: 1, layout: "cards", contextWidth: 288, inspectorWidth: 416 }),
    JSON.stringify({ version: 1, layout: "list", contextWidth: 223, inspectorWidth: 416 }),
    JSON.stringify({ version: 1, layout: "list", contextWidth: 361, inspectorWidth: 416 }),
    JSON.stringify({ version: 1, layout: "list", contextWidth: 288.5, inspectorWidth: 416 }),
    JSON.stringify({ version: 1, layout: "list", contextWidth: 288, inspectorWidth: 299 }),
    JSON.stringify({ version: 1, layout: "list", contextWidth: 288, inspectorWidth: 521 }),
    JSON.stringify({
      version: 1,
      layout: "list",
      contextWidth: 288,
      inspectorWidth: 416,
      repositoryId: "a".repeat(32),
    }),
    '{"version":1,"layout":"list","contextWidth":288,"inspectorWidth":416,"__proto__":{"proof":"secret"}}',
    "x".repeat(4097),
  ])("rejects malformed, unknown, or oversized input", (raw) => {
    expect(decodeBackupAssetsPreferences(raw)).toBeNull();
  });

  it("removes invalid storage and returns safe defaults", () => {
    const storage = {
      getItem: vi.fn(() => '{"version":1,"layout":"grid","query":"secret"}'),
      setItem: vi.fn(),
      removeItem: vi.fn(),
    };

    expect(readBackupAssetsPreferences(storage)).toEqual(DEFAULT_BACKUP_ASSETS_PREFERENCES);
    expect(storage.removeItem).toHaveBeenCalledWith(BACKUP_ASSETS_PREFERENCES_KEY);
  });

  it("fails safely when browser storage is unavailable", () => {
    const storage = {
      getItem: vi.fn(() => {
        throw new DOMException("denied");
      }),
      setItem: vi.fn(() => {
        throw new DOMException("quota");
      }),
      removeItem: vi.fn(),
    };

    expect(readBackupAssetsPreferences(storage)).toEqual(DEFAULT_BACKUP_ASSETS_PREFERENCES);
    expect(writeBackupAssetsPreferences(DEFAULT_BACKUP_ASSETS_PREFERENCES, storage)).toBe(false);
  });

  it("writes only the exact preference record", () => {
    const storage = {
      getItem: vi.fn(),
      setItem: vi.fn(),
      removeItem: vi.fn(),
    };

    expect(writeBackupAssetsPreferences(DEFAULT_BACKUP_ASSETS_PREFERENCES, storage)).toBe(true);
    expect(storage.setItem).toHaveBeenCalledTimes(1);
    const [key, raw] = storage.setItem.mock.calls[0] ?? [];
    expect(key).toBe(BACKUP_ASSETS_PREFERENCES_KEY);
    expect(decodeBackupAssetsPreferences(raw)).toEqual(DEFAULT_BACKUP_ASSETS_PREFERENCES);
    expect(raw).not.toMatch(/query|path|entry|ticket|proof|reason/i);
  });

  it("lets an explicit route layout override the stored preference", () => {
    expect(resolveBackupAssetsLayout("list", { ...DEFAULT_BACKUP_ASSETS_PREFERENCES, layout: "grid" })).toBe(
      "list"
    );
    expect(resolveBackupAssetsLayout(undefined, { ...DEFAULT_BACKUP_ASSETS_PREFERENCES, layout: "grid" })).toBe(
      "grid"
    );
  });
});
