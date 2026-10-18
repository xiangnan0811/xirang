import { execFileSync, spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { lstatSync, mkdirSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { expect, test, type Locator, type Page, type Request, type TestInfo } from "@playwright/test";
import { e2eAdminPassword, freshAuthenticatorCode, loginAs, submitFreshProof } from "./real-backend-session";

// Real-backend acceptance for R-14 system database backup download. Every test
// uses its own admin with its own TOTP enrollment against the shared disposable
// backend started by playwright.real-backend.config.ts. The File System Access
// picker is removed before any page script runs, so these runs prove only the
// bounded compatibility (Blob) save branch; the native chooser is verified
// separately with a real headed Chromium Save dialog.

const REPOSITORY_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const BACKUP_SCRIPT = path.join(REPOSITORY_ROOT, "scripts", "backup-db.sh");
const STEP_UP_PATH = "/api/v1/auth/step-up";
const GRANT_PATH = "/api/v1/credential-access-grants/system-backup-download";
const DOWNLOAD_PATH = /^\/api\/v1\/system\/backups\/(web|cron)\/[^/]+\/download$/;
const COMPAT_LIMIT_BYTES = 64 * 1024 * 1024;
const PADDING_MIB = 96;
const MARKER_VALUE = "r14-roundtrip";
const DIGEST = /^[0-9a-f]{64}$/;
const DOWNLOAD_REASON = "R14 offline restore rehearsal";

type Source = "web" | "cron";
type Session = { secret: string; stepUps: { count: number } };
type Traffic = { stepUps: Request[]; grants: Request[]; downloads: Request[] };
type ListedEntry = { filename: string; size: number; sha256: string; packagedSize: number };
type RuntimePaths = { sqlitePath: string; cronDir: string };
type TarMember = {
  name: string;
  prefix: string;
  linkname: string;
  type: string;
  mode: number;
  uid: number;
  gid: number;
  mtime: number;
  uname: string;
  gname: string;
  body: Buffer;
};

test.describe("system database backup download (compatibility save branch)", () => {
  test("downloads a real Web snapshot and a real cron artifact as verified two-member archives", async ({ page }, testInfo) => {
    test.setTimeout(300_000);
    const runtime = runtimePaths();
    seedMarker(runtime.sqlitePath);
    const session = await beginAdmin(page, testInfo);
    const traffic = watchTraffic(page);
    await openMaintenance(page);

    const snapshot = await createWebSnapshot(page);
    const web = page.getByRole("region", { name: "Web snapshots" });
    await expect(web.locator(`tr[data-backup-row="${snapshot.filename}"]`)).toBeVisible();
    const webEntry = await listedEntry(page, "web", snapshot.filename);
    expect(webEntry.sha256).toBe(snapshot.sha256);
    expect(webEntry.size).toBe(snapshot.size);
    const webArchive = await downloadThroughDialog(page, session, traffic, web, "web", webEntry, testInfo);
    verifyArchive(webArchive, webEntry, "web", testInfo);

    const cronName = await runBackupScript(runtime.cronDir, runtime.sqlitePath);
    const cron = page.getByRole("region", { name: "Cron backup files" });
    await cron.getByRole("button", { name: "Refresh cron backup files", exact: true }).click();
    await expect(cron.locator(`tr[data-backup-row="${cronName}"]`)).toBeVisible();
    const cronEntry = await listedEntry(page, "cron", cronName);
    const cronArchive = await downloadThroughDialog(page, session, traffic, cron, "cron", cronEntry, testInfo);
    verifyArchive(cronArchive, cronEntry, "cron", testInfo);

    // Each download needed its own fresh proof and its own new grant.
    expect(session.stepUps.count).toBe(2);
    expect(traffic.stepUps).toHaveLength(2);
    expect(traffic.grants).toHaveLength(2);
    expect(traffic.downloads).toHaveLength(2);
    const proofs = await Promise.all(traffic.downloads.map(async (request) => (await request.allHeaders())["x-xirang-step-up"] ?? ""));
    expect(proofs.every((proof) => proof !== "")).toBe(true);
    expect(new Set(proofs).size).toBe(2);
  });

  test("refuses a real 96 MiB SQLite backup before any proof, grant, or download request", async ({ page }, testInfo) => {
    test.setTimeout(300_000);
    const runtime = runtimePaths();
    const source = createPaddedSource(testInfo);
    const cronName = await runBackupScript(runtime.cronDir, source);
    const artifact = path.join(runtime.cronDir, cronName);
    // The oversize fixture is a real SQLite backup produced by backup-db.sh, not a sparse file.
    expect(sqliteRows(artifact, "ro", "PRAGMA integrity_check;")).toEqual([{ integrity_check: "ok" }]);
    expect(sqliteRows(artifact, "ro", "SELECT value FROM r14_download_marker WHERE id = 1;")).toEqual([{ value: MARKER_VALUE }]);

    const session = await beginAdmin(page, testInfo);
    const traffic = watchTraffic(page);
    await openMaintenance(page);
    const cron = page.getByRole("region", { name: "Cron backup files" });
    await cron.getByRole("button", { name: "Refresh cron backup files", exact: true }).click();
    await expect(cron.locator(`tr[data-backup-row="${cronName}"]`)).toBeVisible();
    const entry = await listedEntry(page, "cron", cronName);
    expect(entry.size).toBeGreaterThanOrEqual(PADDING_MIB * 1024 * 1024);
    expect(entry.packagedSize).toBeGreaterThan(COMPAT_LIMIT_BYTES);

    const button = cron.getByRole("button", { name: `Download ${cronName}`, exact: true });
    await button.click();
    const dialog = page.getByRole("dialog", { name: "Download database backup" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(cronName);
    await expect(dialog).toContainText("This backup is larger than 64 MiB");
    await expect(dialog.getByRole("button", { name: "Verify and download", exact: true })).toBeDisabled();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(button).toBeFocused();

    await expect(page.getByRole("dialog", { name: "Additional verification required" })).toHaveCount(0);
    expect(session.stepUps.count).toBe(0);
    expect(traffic.stepUps).toHaveLength(0);
    expect(traffic.grants).toHaveLength(0);
    expect(traffic.downloads).toHaveLength(0);
  });

  test("cancelling before or during verification sends nothing and a retry needs a fresh proof", async ({ page }, testInfo) => {
    test.setTimeout(300_000);
    const runtime = runtimePaths();
    seedMarker(runtime.sqlitePath);
    const session = await beginAdmin(page, testInfo);
    const traffic = watchTraffic(page);
    await openMaintenance(page);
    const snapshot = await createWebSnapshot(page);
    const web = page.getByRole("region", { name: "Web snapshots" });
    await expect(web.locator(`tr[data-backup-row="${snapshot.filename}"]`)).toBeVisible();
    const entry = await listedEntry(page, "web", snapshot.filename);
    const button = web.getByRole("button", { name: `Download ${snapshot.filename}`, exact: true });
    const dialog = page.getByRole("dialog", { name: "Download database backup" });
    const stepUp = page.getByRole("dialog", { name: "Additional verification required" });
    const confirm = dialog.getByRole("button", { name: "Verify and download", exact: true });

    await button.click();
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(dialog).toBeHidden();
    await expect(button).toBeFocused();

    await button.click();
    await expect(dialog).toBeVisible();
    await confirm.click();
    await expect(dialog.getByRole("alert").filter({ hasText: "Enter a reason for the download." })).toBeVisible();
    await expect(stepUp).toHaveCount(0);

    const reason = dialog.getByLabel("Download reason");
    await reason.fill(DOWNLOAD_REASON);
    await confirm.click();
    await expect(stepUp).toBeVisible({ timeout: 15_000 });
    await stepUp.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(stepUp).toBeHidden();
    await expect(dialog.getByRole("alert")).toBeVisible();
    // No automatic re-prompt or replay after the cancellation.
    await expect(stepUp).toHaveCount(0);
    expect(traffic.stepUps).toHaveLength(0);
    expect(traffic.grants).toHaveLength(0);
    expect(traffic.downloads).toHaveLength(0);

    await dialog.getByRole("button", { name: "Try again", exact: true }).click();
    await expect(reason).toHaveValue(DOWNLOAD_REASON);
    await expect(stepUp).toHaveCount(0);
    const archive = await confirmAndSave(page, session, traffic, dialog, "web", entry, testInfo);
    expect(session.stepUps.count).toBe(1);
    expect(traffic.grants).toHaveLength(1);
    expect(traffic.downloads).toHaveLength(1);
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await expect(dialog).toBeHidden();
    await expect(button).toBeFocused();
    verifyArchive(archive, entry, "web", testInfo);

    // The same row again is a new attempt: a fresh proof and a new grant, never a cached one.
    await downloadThroughDialog(page, session, traffic, web, "web", entry, testInfo);
    expect(session.stepUps.count).toBe(2);
    expect(traffic.grants).toHaveLength(2);
    expect(traffic.downloads).toHaveLength(2);
  });
});

async function beginAdmin(page: Page, testInfo: TestInfo): Promise<Session> {
  const seed = `r14:${testInfo.project.name}:${testInfo.testId}`;
  const token = createHash("sha256").update(`${seed}:${randomBytes(4).toString("hex")}`).digest("hex").slice(0, 8);
  const client = createHash("sha256").update(seed).digest("hex");
  await page.setExtraHTTPHeaders({ "X-Forwarded-For": `2001:db8:${client.slice(0, 4)}:${client.slice(4, 8)}::14` });
  await page.addInitScript(() => {
    // Headless acceptance exercises compatibility only, not a native directory chooser.
    const holder = window as unknown as Record<string, unknown>;
    try {
      delete holder.showDirectoryPicker;
    } catch {
      // Non-configurable globals are shadowed below.
    }
    Object.defineProperty(window, "showDirectoryPicker", { value: undefined, configurable: true, writable: true });
  });
  testInfo.annotations.push({ type: "save-branch", description: "compatibility (showDirectoryPicker removed)" });
  const stepUps = { count: 0 };
  page.on("response", (response) => {
    if (new URL(response.url()).pathname === STEP_UP_PATH) stepUps.count += 1;
  });
  const username = `e2e-r14-admin-${token}`;
  insertIndependentAdmin(runtimePaths().sqlitePath, username);
  await loginAs(page, username, e2eAdminPassword());
  const secret = await activateTotp(page);
  return { secret, stepUps };
}

function watchTraffic(page: Page): Traffic {
  const traffic: Traffic = { stepUps: [], grants: [], downloads: [] };
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (request.method() === "POST" && url.pathname === STEP_UP_PATH) traffic.stepUps.push(request);
    else if (request.method() === "POST" && url.pathname === GRANT_PATH) traffic.grants.push(request);
    else if (DOWNLOAD_PATH.test(url.pathname)) traffic.downloads.push(request);
  });
  return traffic;
}

async function activateTotp(page: Page): Promise<string> {
  await page.goto("/app/tasks");
  await page.getByRole("link", { name: "Enable two-factor authentication", exact: true }).click();
  await expect(page).toHaveURL(/\/app\/settings\?tab=account$/);
  await page.getByRole("button", { name: "Enable 2FA", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Setup Two-Factor Authentication" });
  const secretElement = dialog.locator("p.font-mono");
  await expect(secretElement).toHaveText(/^[A-Z2-7]+$/);
  const secret = (await secretElement.innerText()).trim();
  await dialog.getByRole("button", { name: "Next", exact: true }).click();
  await dialog.getByLabel("Verification code").fill(await freshAuthenticatorCode(secret));
  const verified = page.waitForResponse((response) =>
    new URL(response.url()).pathname === "/api/v1/auth/2fa/verify" && response.request().method() === "POST");
  await dialog.getByRole("button", { name: "Verify and Enable", exact: true }).click();
  expect((await verified).ok()).toBe(true);
  await dialog.getByRole("checkbox", { name: "I have saved my recovery codes", exact: true }).check();
  await dialog.getByRole("button", { name: "Finish", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Disable 2FA", exact: true })).toBeVisible();
  return secret;
}

async function openMaintenance(page: Page): Promise<void> {
  await page.goto("/app/settings?tab=maintenance");
  await expect(page.getByRole("heading", { name: "Maintenance", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Web snapshots" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Cron backup files" })).toBeVisible();
  const capability = await page.evaluate(() => ({
    secure: window.isSecureContext,
    picker: typeof (window as unknown as Record<string, unknown>).showDirectoryPicker,
  }));
  expect(capability).toEqual({ secure: true, picker: "undefined" });
}

async function createWebSnapshot(page: Page): Promise<{ filename: string; size: number; sha256: string }> {
  const posted = page.waitForResponse((response) =>
    response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/system/backup-db");
  await page.locator("#settings-panel-maintenance").getByRole("button", { name: "Backup Now", exact: true }).click();
  const response = await posted;
  const text = await response.text();
  expect(response.status(), text).toBe(200);
  const data = record(record(JSON.parse(text), "backup create").data, "backup create data");
  if (typeof data.filename !== "string" || typeof data.size !== "number" || typeof data.sha256 !== "string") {
    throw new Error("backup create response is missing filename, size, or sha256");
  }
  expect(data.filename).toMatch(/^xirang-\d{8}-\d{6}(-[A-Za-z0-9_-]{1,64})?\.db$/);
  expect(data.sha256).toMatch(DIGEST);
  return { filename: data.filename, size: data.size, sha256: data.sha256 };
}

// Reads the real list endpoint with the page's own session so the assertion uses
// the exact identity the panel was given, not a value derived by the test.
async function listedEntry(page: Page, source: Source, filename: string): Promise<ListedEntry> {
  const result = await page.evaluate(async (query) => {
    const token = sessionStorage.getItem("xirang-auth-token") ?? "";
    const response = await fetch(`/api/v1/system/backups${query}`, { headers: { Authorization: `Bearer ${token}` }, cache: "no-store" });
    return { status: response.status, body: await response.text() };
  }, source === "cron" ? "?source=cron" : "");
  expect(result.status, result.body.slice(0, 300)).toBe(200);
  const rows = record(JSON.parse(result.body), "backup list").data;
  if (!Array.isArray(rows)) throw new Error("backup list data is not an array");
  const matches = rows.filter((row) => record(row, "backup row").filename === filename);
  expect(matches).toHaveLength(1);
  const row = record(matches[0], "backup row");
  expect(row.source).toBe(source);
  expect(row.engine).toBe("sqlite");
  expect(row.downloadable).toBe(true);
  expect(row.unavailable_reason).toBe("");
  if (typeof row.size !== "number" || typeof row.sha256 !== "string" || typeof row.packaged_size !== "number") {
    throw new Error("backup row is missing size, sha256, or packaged_size");
  }
  expect(row.sha256).toMatch(DIGEST);
  expect(row.packaged_size).toBe(2560 + Math.ceil(row.size / 512) * 512);
  return { filename, size: row.size, sha256: row.sha256, packagedSize: row.packaged_size };
}

async function downloadThroughDialog(
  page: Page,
  session: Session,
  traffic: Traffic,
  region: Locator,
  source: Source,
  entry: ListedEntry,
  testInfo: TestInfo,
): Promise<Buffer> {
  const button = region.getByRole("button", { name: `Download ${entry.filename}`, exact: true });
  await expect(button).toBeEnabled();
  await button.click();
  const dialog = page.getByRole("dialog", { name: "Download database backup" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText(entry.filename);
  await dialog.getByLabel("Download reason").fill(DOWNLOAD_REASON);
  const archive = await confirmAndSave(page, session, traffic, dialog, source, entry, testInfo);
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(button).toBeFocused();
  return archive;
}

async function confirmAndSave(
  page: Page,
  session: Session,
  traffic: Traffic,
  dialog: Locator,
  source: Source,
  entry: ListedEntry,
  testInfo: TestInfo,
): Promise<Buffer> {
  const grantsBefore = traffic.grants.length;
  const downloadsBefore = traffic.downloads.length;
  const saved = page.waitForEvent("download", { timeout: 120_000 });
  const served = page.waitForResponse((response) => DOWNLOAD_PATH.test(new URL(response.url()).pathname), { timeout: 120_000 });
  await dialog.getByRole("button", { name: "Verify and download", exact: true }).click();
  await submitFreshProof(page, session.secret, session.stepUps);
  const response = await served;
  expect(response.status()).toBe(200);
  const headers = response.headers();
  expect(headers["content-type"]).toBe("application/x-tar");
  expect(headers["content-encoding"] ?? "").toBe("");
  expect(Number(headers["content-length"])).toBe(entry.packagedSize);
  expect(headers["etag"]).toBe(`"${entry.sha256}"`);
  expect(headers["cache-control"]).toContain("no-store");

  expect(traffic.grants).toHaveLength(grantsBefore + 1);
  const grant = traffic.grants[grantsBefore];
  expect(new URL(grant.url()).search).toBe("");
  expect(grant.postDataJSON()).toMatchObject({ reason: DOWNLOAD_REASON });
  expect(traffic.downloads).toHaveLength(downloadsBefore + 1);
  const request = traffic.downloads[downloadsBefore];
  const url = new URL(request.url());
  expect(url.pathname).toBe(`/api/v1/system/backups/${source}/${encodeURIComponent(entry.filename)}/download`);
  expect(url.search).toBe("");
  expect(request.method()).toBe("GET");
  const sent = await request.allHeaders();
  expect(sent["if-match"]).toBe(`"${entry.sha256}"`);
  expect(sent["authorization"]).toMatch(/^Bearer .+/);
  expect(sent["x-xirang-step-up"] ?? "").not.toBe("");

  const download = await saved;
  expect(download.suggestedFilename()).toBe(`${source}-${entry.filename}.tar`);
  const target = testInfo.outputPath(`${source}-${entry.filename}-${downloadsBefore}.tar`);
  await download.saveAs(target);
  // Exact match: the compatibility notice also contains "handed to the browser download".
  await expect(dialog.getByText("Handed to the browser download. Check the browser's downloads to confirm it finished.", { exact: true }))
    .toBeVisible();
  await testInfo.attach(`${source}-archive-path`, { body: target, contentType: "text/plain" });
  return readFileSync(target);
}

function verifyArchive(archive: Buffer, entry: ListedEntry, source: Source, testInfo: TestInfo): void {
  expect(archive.length).toBe(entry.packagedSize);
  const members = readUstar(archive);
  expect(members.map((member) => member.name)).toEqual([entry.filename, `${entry.filename}.sha256`]);
  for (const member of members) {
    expect({
      prefix: member.prefix,
      linkname: member.linkname,
      type: member.type,
      mode: member.mode,
      uid: member.uid,
      gid: member.gid,
      mtime: member.mtime,
      uname: member.uname,
      gname: member.gname,
    }).toEqual({ prefix: "", linkname: "", type: "0", mode: 0o600, uid: 0, gid: 0, mtime: 0, uname: "", gname: "" });
  }
  const [data, sidecar] = members;
  expect(data.body.length).toBe(entry.size);
  expect(createHash("sha256").update(data.body).digest("hex")).toBe(entry.sha256);
  expect(sidecar.body.toString("utf8")).toBe(`${entry.sha256}  ${entry.filename}\n`);

  const directory = testInfo.outputPath(`${source}-extracted-${randomBytes(4).toString("hex")}`);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  writeFileSync(path.join(directory, data.name), data.body, { flag: "wx", mode: 0o600 });
  writeFileSync(path.join(directory, sidecar.name), sidecar.body, { flag: "wx", mode: 0o600 });
  // A nonzero exit rejects a mismatched sidecar; localized success wording is not a contract.
  execFileSync("sha256sum", ["--strict", "-c", sidecar.name], { cwd: directory, stdio: "pipe", timeout: 60_000 });
  const database = path.join(directory, data.name);
  expect(sqliteRows(database, "ro", "PRAGMA integrity_check;")).toEqual([{ integrity_check: "ok" }]);
  expect(sqliteRows(database, "ro", "SELECT value FROM r14_download_marker WHERE id = 1;")).toEqual([{ value: MARKER_VALUE }]);
}

// Minimal strict USTAR reader: verifies header checksums, magic, and the
// two-zero-block terminator so an unterminated or padded stream fails.
function readUstar(archive: Buffer): TarMember[] {
  expect(archive.length % 512).toBe(0);
  const members: TarMember[] = [];
  let offset = 0;
  for (;;) {
    const header = archive.subarray(offset, offset + 512);
    if (header.length !== 512) throw new Error("archive ended without the end-of-archive blocks");
    if (header.every((byte) => byte === 0)) {
      const trailer = archive.subarray(offset);
      expect(trailer.length).toBe(1024);
      expect(trailer.every((byte) => byte === 0)).toBe(true);
      return members;
    }
    const text = (start: number, end: number): string => {
      const raw = header.subarray(start, end);
      const nul = raw.indexOf(0);
      return raw.subarray(0, nul < 0 ? raw.length : nul).toString("utf8");
    };
    const octal = (start: number, end: number): number => {
      const value = text(start, end).trim();
      if (!/^[0-7]+$/.test(value)) throw new Error(`tar header field ${start} is not octal`);
      return Number.parseInt(value, 8);
    };
    let checksum = 0;
    for (let index = 0; index < 512; index += 1) checksum += index >= 148 && index < 156 ? 32 : header[index];
    expect(octal(148, 156)).toBe(checksum);
    expect(header.subarray(257, 263).toString("latin1")).toBe("ustar\0");
    expect(header.subarray(263, 265).toString("latin1")).toBe("00");
    const size = octal(124, 136);
    const bodyStart = offset + 512;
    if (bodyStart + size > archive.length) throw new Error("tar member body is truncated");
    members.push({
      name: text(0, 100),
      prefix: text(345, 500),
      linkname: text(157, 257),
      type: String.fromCharCode(header[156]),
      mode: octal(100, 108),
      uid: octal(108, 116),
      gid: octal(116, 124),
      mtime: octal(136, 148),
      uname: text(265, 297),
      gname: text(297, 329),
      body: archive.subarray(bodyStart, bodyStart + size),
    });
    offset = bodyStart + Math.ceil(size / 512) * 512;
  }
}

function runtimePaths(): RuntimePaths {
  const runtimeDir = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  if (!path.isAbsolute(runtimeDir)) throw new Error("E2E_RUNTIME_DIR must be absolute");
  const runtimeReal = realpathSync(runtimeDir);
  const fixturePath = path.join(runtimeReal, "fixture.json");
  const fixtureInfo = lstatSync(fixturePath);
  if (fixtureInfo.isSymbolicLink() || !fixtureInfo.isFile()) throw new Error("isolated runtime fixture must be a regular file");
  const parsed = record(JSON.parse(readFileSync(fixturePath, "utf8")), "runtime fixture");
  const sqlitePath = parsed.sqlitePath;
  if (typeof sqlitePath !== "string" || !path.isAbsolute(sqlitePath)) throw new Error("isolated runtime sqlite path is invalid");
  const sqliteInfo = lstatSync(sqlitePath);
  if (sqliteInfo.isSymbolicLink() || !sqliteInfo.isFile()) throw new Error("isolated runtime database must be a regular file");
  // e2e-backend.sh fixes the cron root at <runtime>/cron-backups and passes it as CRON_DB_BACKUP_DIR.
  const cronDir = path.join(runtimeReal, "cron-backups");
  const cronInfo = lstatSync(cronDir);
  if (cronInfo.isSymbolicLink() || !cronInfo.isDirectory()) throw new Error("isolated cron backup root must be a real directory");
  for (const candidate of [realpathSync(sqlitePath), realpathSync(cronDir)]) {
    if (path.dirname(candidate) !== runtimeReal) throw new Error("isolated runtime path escaped the runtime");
  }
  return { sqlitePath: realpathSync(sqlitePath), cronDir: realpathSync(cronDir) };
}

function seedMarker(sqlitePath: string): void {
  sqliteRows(sqlitePath, "rw", [
    "CREATE TABLE IF NOT EXISTS r14_download_marker (id INTEGER PRIMARY KEY, value TEXT NOT NULL);",
    `INSERT OR REPLACE INTO r14_download_marker (id, value) VALUES (1, '${MARKER_VALUE}');`,
  ].join("\n"));
}

// A separate real SQLite source with ~96 MiB of actual BLOB rows; backup-db.sh
// snapshots it into the managed cron root like any other cron run.
function createPaddedSource(testInfo: TestInfo): string {
  const source = testInfo.outputPath(`padding-source-${randomBytes(4).toString("hex")}.db`);
  mkdirSync(path.dirname(source), { recursive: true });
  sqliteRows(source, "rwc", [
    "CREATE TABLE r14_download_marker (id INTEGER PRIMARY KEY, value TEXT NOT NULL);",
    `INSERT INTO r14_download_marker (id, value) VALUES (1, '${MARKER_VALUE}');`,
    "CREATE TABLE r14_download_padding (value BLOB);",
    `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ${PADDING_MIB})`,
    "INSERT INTO r14_download_padding (value) SELECT randomblob(1048576) FROM n;",
  ].join("\n"));
  expect(lstatSync(source).size).toBeGreaterThanOrEqual(PADDING_MIB * 1024 * 1024);
  return source;
}

// Runs the shipped producer exactly as cron would, into the backend's managed
// cron root. Its names have one-second resolution and it refuses to overwrite,
// so a same-second collision is retried on the next second.
async function runBackupScript(outputDir: string, sqlitePath: string): Promise<string> {
  const env = { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: process.env.HOME ?? "/tmp", LANG: "C.UTF-8", DB_TYPE: "sqlite", SQLITE_PATH: sqlitePath };
  for (let attempt = 0; ; attempt += 1) {
    const result = spawnSync("bash", [BACKUP_SCRIPT, outputDir], { cwd: REPOSITORY_ROOT, env, encoding: "utf8", timeout: 180_000 });
    if (result.status === 0) {
      const produced = /^BACKUP_FILE=(.+)$/m.exec(result.stdout)?.[1] ?? "";
      if (path.dirname(produced) !== outputDir) throw new Error("backup-db.sh wrote outside the managed cron root");
      const name = path.basename(produced);
      expect(name).toMatch(/^xirang-sqlite-\d{8}-\d{6}\.db$/);
      return name;
    }
    if (attempt < 3 && result.stderr.includes("拒绝覆盖")) {
      await delay(1_100);
      continue;
    }
    throw new Error(`backup-db.sh exited ${String(result.status)}: ${result.stderr.slice(0, 400)}`);
  }
}

function insertIndependentAdmin(dbPath: string, username: string): void {
  if (!/^e2e-r14-admin-[0-9a-f]{8}$/.test(username)) throw new Error("refusing unexpected independent username");
  const rows = sqliteRows(dbPath, "rw", [
    "INSERT INTO users (",
    "username, password_hash, role, totp_secret, totp_enabled, recovery_codes,",
    "token_version, totp_enrollment_id, totp_enrollment_expires_at, onboarded,",
    "created_at, updated_at",
    ") SELECT",
    `'${username}',`,
    "(SELECT password_hash FROM users WHERE username = 'admin'),",
    "'admin', '', 0, '', 0, '', NULL, 1, datetime('now'), datetime('now')",
    "WHERE (SELECT COUNT(*) FROM users WHERE username = 'admin') = 1",
    `AND (SELECT COUNT(*) FROM users WHERE username = '${username}') = 0;`,
    "SELECT changes() AS changes,",
    `(SELECT role FROM users WHERE username = '${username}') AS role,`,
    `(SELECT totp_enabled FROM users WHERE username = '${username}') AS totp_enabled;`,
  ].join("\n"));
  const row = rows[0];
  if (rows.length !== 1 || row === undefined || row.changes !== 1 || row.role !== "admin" || row.totp_enabled !== 0) {
    throw new Error(`independent admin ${username} was not inserted`);
  }
}

function sqliteRows(dbPath: string, mode: "ro" | "rw" | "rwc", sql: string): Record<string, unknown>[] {
  const uri = pathToFileURL(path.resolve(dbPath));
  uri.searchParams.set("mode", mode);
  let output: string;
  try {
    output = execFileSync("sqlite3", ["-json", "-batch", "-bail", "-init", "/dev/null", "-cmd", ".timeout 5000", uri.href, sql], {
      encoding: "utf8",
      timeout: 120_000,
      maxBuffer: 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
    }).trim();
  } catch (error) {
    throw new Error((error instanceof Error ? error.message : "sqlite3 failed").slice(0, 400));
  }
  if (output === "") return [];
  const parsed: unknown = JSON.parse(output);
  if (!Array.isArray(parsed)) throw new Error("sqlite json result is not an array");
  return parsed.map((row) => record(row, "sqlite row"));
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} is not an object`);
  return value as Record<string, unknown>;
}
