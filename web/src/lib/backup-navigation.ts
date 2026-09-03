import { redirect } from "react-router-dom";

export const BACKUP_FILES_PATH = "/app/backups/data";

const BACKUP_INDEX = "/app/backups";
const BACKUP_OVERVIEW = "/app/backups/overview";
const BACKUP_RECOVERY = "/app/backups/recovery";
const BACKUP_SECTION_PATHS: Record<string, true> = {
  [BACKUP_INDEX]: true,
  [BACKUP_FILES_PATH]: true,
  [BACKUP_OVERVIEW]: true,
  [BACKUP_RECOVERY]: true,
};
const BACKUP_TABS = [BACKUP_FILES_PATH, BACKUP_OVERVIEW, BACKUP_RECOVERY] as const;

export type BackupRouteTab = "data" | "overview" | "recovery";

export function normalizeAppPathname(pathname: string): string {
  if (pathname.length <= 1) {
    return pathname || "/";
  }
  return pathname.replace(/\/+$/, "") || "/";
}

export function isNavPathActive(itemPath: string, pathname: string): boolean {
  const current = normalizeAppPathname(pathname);
  const target = normalizeAppPathname(itemPath);
  if (BACKUP_SECTION_PATHS[target]) {
    return BACKUP_SECTION_PATHS[current] === true;
  }
  return current === target || current.startsWith(`${target}/`);
}

export function backupFilesHref(pathname: string, search: string): string {
  return `${BACKUP_FILES_PATH}${normalizeAppPathname(pathname) === BACKUP_FILES_PATH ? search : ""}`;
}

export function getBackupActivePage(pathname: string): BackupRouteTab {
  const normalized = normalizeAppPathname(pathname);
  if (normalized === BACKUP_OVERVIEW) return "overview";
  if (normalized === BACKUP_RECOVERY) return "recovery";
  return "data";
}

export function canonicalizeBackupLocation({ request }: { request: Request }) {
  const url = new URL(request.url);
  const normalized = normalizeAppPathname(url.pathname);
  if (normalized === BACKUP_INDEX) {
    return redirect(`${BACKUP_FILES_PATH}${url.search}`);
  }
  if ((BACKUP_TABS as readonly string[]).includes(normalized) && url.pathname !== normalized) {
    return redirect(`${normalized}${url.search}`);
  }
  return null;
}
