import type { BackupAssetsLayout } from "./backup-assets-route-state";

export const BACKUP_ASSETS_PREFERENCES_KEY = "xirang.backup-assets.preferences.v1";
const MAX_PREFERENCE_BYTES = 4096;

export interface BackupAssetsPreferencesV1 {
  version: 1;
  layout: BackupAssetsLayout;
  contextWidth: number;
  inspectorWidth: number;
}

export const DEFAULT_BACKUP_ASSETS_PREFERENCES: BackupAssetsPreferencesV1 = {
  version: 1,
  layout: "list",
  contextWidth: 288,
  inspectorWidth: 416,
};

interface PreferenceStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export function decodeBackupAssetsPreferences(raw: string | null): BackupAssetsPreferencesV1 | null {
  if (raw === null || raw.length === 0 || raw.length > MAX_PREFERENCE_BYTES) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || Object.getPrototypeOf(parsed) !== Object.prototype) return null;
    const keys = Object.keys(parsed).sort();
    if (keys.join(",") !== "contextWidth,inspectorWidth,layout,version") return null;
    if (
      parsed.version !== 1 ||
      (parsed.layout !== "list" && parsed.layout !== "grid") ||
      !isIntegerInRange(parsed.contextWidth, 224, 360) ||
      !isIntegerInRange(parsed.inspectorWidth, 300, 520)
    ) {
      return null;
    }
    return {
      version: 1,
      layout: parsed.layout,
      contextWidth: parsed.contextWidth,
      inspectorWidth: parsed.inspectorWidth,
    };
  } catch {
    return null;
  }
}

export function readBackupAssetsPreferences(
  storage: PreferenceStorage | null = getLocalStorage()
): BackupAssetsPreferencesV1 {
  if (storage === null) return { ...DEFAULT_BACKUP_ASSETS_PREFERENCES };
  try {
    const decoded = decodeBackupAssetsPreferences(storage.getItem(BACKUP_ASSETS_PREFERENCES_KEY));
    if (decoded !== null) return decoded;
    storage.removeItem(BACKUP_ASSETS_PREFERENCES_KEY);
  } catch {
    return { ...DEFAULT_BACKUP_ASSETS_PREFERENCES };
  }
  return { ...DEFAULT_BACKUP_ASSETS_PREFERENCES };
}

export function writeBackupAssetsPreferences(
  preferences: BackupAssetsPreferencesV1,
  storage: PreferenceStorage | null = getLocalStorage()
): boolean {
  if (storage === null) return false;
  const raw = JSON.stringify(preferences);
  if (decodeBackupAssetsPreferences(raw) === null || raw.length > MAX_PREFERENCE_BYTES) return false;
  try {
    storage.setItem(BACKUP_ASSETS_PREFERENCES_KEY, raw);
    return true;
  } catch {
    return false;
  }
}

export function resolveBackupAssetsLayout(
  routeLayout: BackupAssetsLayout | undefined,
  preferences: BackupAssetsPreferencesV1
): BackupAssetsLayout {
  return routeLayout ?? preferences.layout;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isIntegerInRange(value: unknown, minimum: number, maximum: number): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= minimum && value <= maximum;
}

function getLocalStorage(): PreferenceStorage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}
