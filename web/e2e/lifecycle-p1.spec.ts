import { execFileSync } from "node:child_process";
import { createHash, createHmac } from "node:crypto";
import { existsSync, lstatSync, readdirSync, readFileSync, realpathSync, statSync, unlinkSync, utimesSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { expect, test, type Locator, type Page, type Response, type Route, type WebSocket } from "@playwright/test";

test.beforeEach(async ({ page }, testInfo) => {
  // The loopback Vite proxy is trusted by the isolated backend. Give each
  // scenario its own documented client address so real per-IP rate limits
  // remain enabled without sharing a bucket across independent scenarios.
  const client = createHash("sha256").update(testInfo.testId).digest("hex");
  await page.setExtraHTTPHeaders({
    "X-Forwarded-For": `2001:db8:${client.slice(0, 4)}:${client.slice(4, 8)}::1`,
  });
});

async function login(page: Page) {
  await page.addInitScript(() => localStorage.setItem("xirang.language", "en"));
  await page.goto("/login");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password").fill(process.env.E2E_ADMIN_PASSWORD ?? "FAKE_E2E_AdminPass2026!_FOR_TEST_ONLY");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/app\/overview$/);
  const wizard = page.getByRole("dialog").filter({ has: page.getByRole("button", { name: "Start Setup", exact: true }) });
  await expect(wizard).toBeVisible({ timeout: 5_000 });
  await page.keyboard.press("Escape");
  await expect(wizard).toBeHidden({ timeout: 5_000 });
}

for (const [timezoneId, localStart] of [
  ["Asia/Singapore", "2026-10-07T11:00"],
  ["UTC", "2026-10-07T03:00"],
  ["America/New_York", "2026-10-06T23:00"],
] as const) {
  test.describe(`silence windows in ${timezoneId}`, () => {
    test.use({ timezoneId });
    test("preserves the selected instant through the real API and readback", async ({ page }) => {
      await login(page);
      await page.clock.setFixedTime(new Date("2026-10-07T03:00:00Z"));
      await page.goto("/app/settings?tab=silences");
      await page.getByRole("button", { name: "New Silence Rule", exact: true }).click();
      await expect(page.locator("#silence-starts")).toHaveValue(localStart);
      const name = `P1 timezone ${timezoneId}`;
      await page.locator("#silence-name").fill(name);
      const responsePromise = page.waitForResponse(response =>
        new URL(response.url()).pathname === "/api/v1/silences" && response.request().method() === "POST");
      await page.locator('[role="dialog"] button[type="submit"]').click();
      const response = await responsePromise;
      expect(response.ok()).toBe(true);
      expect(response.request().postDataJSON()).toMatchObject({
        starts_at: "2026-10-07T03:00:00.000Z",
        ends_at: "2026-10-07T04:00:00.000Z",
      });
      const created = await response.json() as { data: { id: number } };
      const saved = await page.evaluate(async (id) => {
        const headers = { Authorization: `Bearer ${sessionStorage.getItem("xirang-auth-token")}` };
        const result = await fetch("/api/v1/silences", { headers });
        const body = await result.json() as { data: { id: number; starts_at: string; ends_at: string }[] };
        return { status: result.status, row: body.data.find(row => row.id === id) };
      }, created.data.id);
      expect(saved.status).toBe(200);
      expect(Date.parse(saved.row!.starts_at)).toBe(Date.parse("2026-10-07T03:00:00Z"));
      expect(Date.parse(saved.row!.ends_at)).toBe(Date.parse("2026-10-07T04:00:00Z"));
    });
  });
}

function authenticatorCode(secret: string, now = Date.now()): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const letter of secret.replace(/=+$/, "").toUpperCase()) {
    const value = alphabet.indexOf(letter);
    if (value < 0) throw new Error("Invalid test TOTP secret");
    bits += value.toString(2).padStart(5, "0");
  }
  const key = Buffer.from((bits.match(/.{8}/g) ?? []).map(byte => Number.parseInt(byte, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(now / 30_000)));
  const digest = createHmac("sha1", key).update(counter).digest();
  const offset = digest[digest.length - 1] & 15;
  return ((digest.readUInt32BE(offset) & 0x7fffffff) % 1_000_000).toString().padStart(6, "0");
}

const CRON_STATUSES = new Set([
  "not_configured",
  "invalid_configuration",
  "directory_unreadable",
  "no_complete_backup",
  "scan_limit_exceeded",
  "clock_anomaly",
  "stale",
  "fresh",
]);
const OWN_CRON_ARTIFACT = "xirang-sqlite-20261007-153000.db";
const OWN_CRON_CHECKSUM = `${OWN_CRON_ARTIFACT}.sha256`;

type CronStatusName =
  | "not_configured"
  | "invalid_configuration"
  | "directory_unreadable"
  | "no_complete_backup"
  | "scan_limit_exceeded"
  | "clock_anomaly"
  | "stale"
  | "fresh";

type CronStatus = {
  status: CronStatusName;
  engine: string;
  checked_at: string;
  max_age_seconds: number;
  directory: string;
  latest_complete_at: string;
  artifact_name: string;
  evidence: string;
  time_source: string;
  content_verified: boolean;
};

type ListedBackup = { filename: string; size: number; sha256: string };
type CreatedBackup = ListedBackup & { path: string };

function errorCode(error: unknown): string {
  return error && typeof error === "object" && "code" in error ? String(error.code) : "unknown";
}

function expectedMaxAgeSeconds(): number {
  const raw = (process.env.CRON_DB_BACKUP_MAX_AGE_HOURS ?? "").trim();
  if (raw === "") return 26 * 60 * 60;
  if (!/^[1-9]\d*$/.test(raw)) {
    throw new Error("CRON_DB_BACKUP_MAX_AGE_HOURS must be a positive integer from 1 to 8760");
  }
  const hours = Number(raw);
  if (hours > 8760) throw new Error("CRON_DB_BACKUP_MAX_AGE_HOURS must be at most 8760");
  return hours * 60 * 60;
}

function assertNoSymlinkThrough(leaf: string, boundary: string): void {
  let current = path.resolve(leaf);
  const stop = path.resolve(boundary);
  for (;;) {
    let info;
    try {
      info = lstatSync(current);
    } catch (error) {
      throw new Error(`${current} is not available (${errorCode(error)})`);
    }
    if (info.isSymbolicLink()) throw new Error(`${current} must not be a symlink`);
    if (current === stop) return;
    const parent = path.dirname(current);
    if (parent === current) throw new Error(`${leaf} is outside ${boundary}`);
    current = parent;
  }
}

function assertAgentCronFixture(directory: string): string {
  if (!path.isAbsolute(directory)) throw new Error("cron fixture directory must be absolute");
  const scratch = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../.tmp/agent");
  assertNoSymlinkThrough(directory, path.dirname(scratch));
  let scratchInfo;
  try {
    scratchInfo = lstatSync(scratch);
  } catch (error) {
    throw new Error(`.tmp/agent is not available for the cron fixture (${errorCode(error)})`);
  }
  if (scratchInfo.isSymbolicLink() || !scratchInfo.isDirectory()) {
    throw new Error(".tmp/agent must be a real directory");
  }
  const scratchReal = realpathSync(scratch);
  let info;
  try {
    info = lstatSync(directory);
  } catch (error) {
    throw new Error(`cron fixture directory is not an existing directory (${errorCode(error)})`);
  }
  if (info.isSymbolicLink()) throw new Error("cron fixture directory must not be a symlink");
  if (!info.isDirectory()) throw new Error("cron fixture directory must be a directory");
  const resolved = realpathSync(directory);
  const relative = path.relative(scratchReal, resolved);
  if (relative === "" || relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
    throw new Error("cron fixture directory must be a subdirectory of .tmp/agent");
  }
  return resolved;
}

function explicitCronFixture(): string | null {
  const runtime = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  if (runtime === "") return null;
  return assertAgentCronFixture(path.join(runtime, "cron-backups"));
}

function removeOwnedCronFile(file: string) {
  const name = path.basename(file);
  if (name !== OWN_CRON_ARTIFACT && name !== OWN_CRON_CHECKSUM) {
    throw new Error("refusing to delete an unexpected cron fixture name");
  }
  assertAgentCronFixture(path.dirname(file));
  let info;
  try {
    info = lstatSync(file);
  } catch (error) {
    if (errorCode(error) === "ENOENT") return;
    throw error;
  }
  if (info.isSymbolicLink() || !info.isFile()) throw new Error(`refusing to delete ${name}`);
  unlinkSync(file);
}

function otherManagedCronArtifacts(directory: string): string[] {
  return readdirSync(directory).filter((name) => name !== OWN_CRON_ARTIFACT && /^xirang-sqlite-\d{8}-\d{6}\.db$/.test(name));
}

function writeCronPair(directory: string, mtime: Date): { files: string[]; digest: string } {
  assertAgentCronFixture(directory);
  const artifact = path.join(directory, OWN_CRON_ARTIFACT);
  const checksum = path.join(directory, OWN_CRON_CHECKSUM);
  const body = Buffer.from("xirang e2e cron observation artifact\n");
  writeFileSync(artifact, body, { flag: "wx", mode: 0o600 });
  const digest = createHash("sha256").update(body).digest("hex");
  try {
    writeFileSync(checksum, `${digest}  ${artifact}\n`, { flag: "wx", mode: 0o600 });
    utimesSync(artifact, mtime, mtime);
    utimesSync(checksum, mtime, mtime);
  } catch (error) {
    removeOwnedCronFile(artifact);
    removeOwnedCronFile(checksum);
    throw error;
  }
  return { files: [artifact, checksum], digest };
}

function pairIdentity(files: string[]): { file: string; size: number; mtimeMs: number }[] {
  return files.map((file) => {
    const info = statSync(file);
    return { file, size: info.size, mtimeMs: info.mtimeMs };
  });
}

function shiftedWholeSecond(deltaSeconds: number): Date {
  return new Date((Math.floor(Date.now() / 1000) + deltaSeconds) * 1000);
}

function isApi(response: Response, method: string, pathname: string): boolean {
  return response.request().method() === method && new URL(response.url()).pathname === pathname;
}

async function readEnvelope(response: Response, label: string): Promise<unknown> {
  const body = await response.text();
  expect(response.status(), body).toBe(200);
  const parsed = JSON.parse(body) as { code?: unknown; data?: unknown };
  expect(parsed.code, body).toBe(200);
  if (parsed.data === undefined) throw new Error(`${label} response is missing data`);
  return parsed.data;
}

function parseCronStatus(data: unknown): CronStatus {
  if (!data || typeof data !== "object") throw new Error("cron backup status response is not an object");
  const row = data as Record<string, unknown>;
  if (typeof row.status !== "string" || !CRON_STATUSES.has(row.status)) {
    throw new Error(`unexpected cron status: ${String(row.status)}`);
  }
  if (typeof row.engine !== "string" || typeof row.checked_at !== "string" || typeof row.max_age_seconds !== "number") {
    throw new Error("cron backup status is missing engine, checked_at, or max_age_seconds");
  }
  if (typeof row.evidence !== "string" || typeof row.time_source !== "string" || typeof row.content_verified !== "boolean") {
    throw new Error("cron backup status is missing evidence fields");
  }
  return {
    status: row.status as CronStatusName,
    engine: row.engine,
    checked_at: row.checked_at,
    max_age_seconds: row.max_age_seconds,
    directory: typeof row.directory === "string" ? row.directory : "",
    latest_complete_at: typeof row.latest_complete_at === "string" ? row.latest_complete_at : "",
    artifact_name: typeof row.artifact_name === "string" ? row.artifact_name : "",
    evidence: row.evidence,
    time_source: row.time_source,
    content_verified: row.content_verified,
  };
}

function parseBackupList(data: unknown): ListedBackup[] {
  if (!Array.isArray(data)) throw new Error("backup list data is not an array");
  return data.map((item) => {
    if (!item || typeof item !== "object") throw new Error("backup list item is not an object");
    const row = item as Record<string, unknown>;
    if (typeof row.filename !== "string" || typeof row.size !== "number" || typeof row.sha256 !== "string") {
      throw new Error("backup list item is missing filename, size, or sha256");
    }
    return { filename: row.filename, size: row.size, sha256: row.sha256 };
  });
}

function parseCreatedBackup(data: unknown): CreatedBackup {
  if (!data || typeof data !== "object") throw new Error("backup create response is not an object");
  const row = data as Record<string, unknown>;
  if (typeof row.filename !== "string" || typeof row.path !== "string" || typeof row.size !== "number" || typeof row.sha256 !== "string") {
    throw new Error("backup create response is missing filename, path, size, or sha256");
  }
  return { filename: row.filename, path: row.path, size: row.size, sha256: row.sha256 };
}

function listedFields(created: CreatedBackup): ListedBackup {
  return { filename: created.filename, size: created.size, sha256: created.sha256 };
}

function expectCronEvidence(cron: CronStatus, status: CronStatusName, maxAgeSeconds: number) {
  expect(cron.status).toBe(status);
  expect(cron.engine).toBe("sqlite");
  expect(cron.evidence).toBe("artifact_pair");
  expect(cron.time_source).toBe("mtime");
  expect(cron.content_verified).toBe(false);
  expect(cron.max_age_seconds).toBe(maxAgeSeconds);
  expect(cron.checked_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/);
  expect(Math.abs(Date.parse(cron.checked_at) - Date.now())).toBeLessThan(5 * 60_000);
}

function waitForMaintenanceReads(page: Page): Promise<{ backups: ListedBackup[]; cron: CronStatus }> {
  const backups = page.waitForResponse((response) => isApi(response, "GET", "/api/v1/system/backups"))
    .then((response) => readEnvelope(response, "backup list").then(parseBackupList));
  const cron = page.waitForResponse((response) => isApi(response, "GET", "/api/v1/system/cron-backup-status"))
    .then((response) => readEnvelope(response, "cron backup status").then(parseCronStatus));
  return Promise.all([backups, cron]).then(([backupList, cronStatus]) => ({ backups: backupList, cron: cronStatus }));
}

function padTime(value: number): string {
  return String(value).padStart(2, "0");
}

function formatLocalInstant(iso: string): string {
  const date = new Date(iso);
  return `${date.getFullYear()}-${padTime(date.getMonth() + 1)}-${padTime(date.getDate())} ${padTime(date.getHours())}:${padTime(date.getMinutes())}:${padTime(date.getSeconds())}`;
}

async function expectSeparateClock(observer: Locator, iso: string) {
  const local = formatLocalInstant(iso);
  const found = await observer.evaluate((root) => {
    const times = [...root.querySelectorAll("time.font-mono")].map((node) => ({
      datetime: node.getAttribute("datetime"),
      text: node.textContent?.trim() ?? "",
    }));
    const localClocks = [...root.querySelectorAll("span")]
      .filter((node) => !node.classList.contains("font-mono"))
      .map((node) => node.textContent?.trim() ?? "");
    return { times, localClocks };
  });
  expect(found.times).toContainEqual({ datetime: iso, text: iso });
  expect(found.localClocks).toContain(local);
}

async function expectObserverEvidence(observer: Locator, cron: CronStatus) {
  if (cron.directory !== "") await expect(observer).toContainText(cron.directory);
  if (cron.artifact_name !== "") await expect(observer).toContainText(cron.artifact_name);
  const wires = await observer.locator("time.font-mono").evaluateAll((nodes) => nodes.map((node) => ({
    datetime: node.getAttribute("datetime"),
    text: node.textContent?.trim() ?? "",
  })));
  const expectedWires = [cron.checked_at];
  if (cron.latest_complete_at !== "") expectedWires.push(cron.latest_complete_at);
  expect(wires.map((row) => row.datetime).sort()).toEqual([...expectedWires].sort());
  for (const row of wires) expect(row.text).toBe(row.datetime);
  await expectSeparateClock(observer, cron.checked_at);
  if (cron.latest_complete_at !== "") await expectSeparateClock(observer, cron.latest_complete_at);
}

async function maintenancePanels(page: Page): Promise<{ web: Locator; observer: Locator }> {
  const stack = page.locator("#settings-panel-maintenance > div.space-y-6");
  await expect(stack.getByRole("heading", { level: 2, name: "Maintenance", exact: true })).toBeVisible();
  const web = stack.getByRole("heading", { level: 3, name: "Self Backup", exact: true })
    .locator("xpath=ancestor::div[contains(@class,'rounded-lg')][1]");
  const observer = stack.locator(':scope > [data-panel="cron-backup-status"]');
  await expect(web).toBeVisible();
  await expect(observer).toBeVisible();
  await expect(observer).toHaveAttribute("aria-labelledby", "cron-backup-evidence-heading");
  await expect(observer.locator("h3#cron-backup-evidence-heading")).toBeVisible();
  await expect(observer.getByRole("button")).toHaveCount(0);
  await expect(web.getByRole("button", { name: "Backup Now", exact: true })).toBeVisible();
  expect(await stack.evaluate((root) => [...root.children].map((child) => {
    if (child.tagName === "H2") return "heading";
    if (child.querySelector("h3")?.textContent?.trim() === "Self Backup") return "self";
    if (child.getAttribute("data-panel") === "cron-backup-status") return "cron";
    if (child.querySelector("h3")?.textContent?.trim() === "Config Import / Export") return "config";
    return child.tagName.toLowerCase();
  }))).toEqual(["heading", "self", "cron", "config"]);
  return { web, observer };
}

// Password-only admin login. Keep this ahead of TOTP activation: that later test
// permanently enables admin 2FA on the shared fresh backend.
test("creates a Web SQLite snapshot and observes cron artifacts without content validation", async ({ page }) => {
  test.setTimeout(180_000);
  const maxAgeSeconds = expectedMaxAgeSeconds();
  const explicit = explicitCronFixture();
  const owned = explicit
    ? [path.join(explicit, OWN_CRON_ARTIFACT), path.join(explicit, OWN_CRON_CHECKSUM)]
    : [];
  const published: string[] = [];
  if (explicit) {
    for (const file of owned) removeOwnedCronFile(file);
    const others = otherManagedCronArtifacts(explicit);
    if (others.length > 0) {
      throw new Error(`cron fixture already contains managed artifacts and was left unchanged: ${others.join(", ")}`);
    }
  }
  test.info().annotations.push({
    type: "cron-observation",
    description: explicit ? "fresh-stale-future" : "default-empty",
  });
  try {
    await login(page);
    const opening = waitForMaintenanceReads(page);
    await page.goto("/app/settings?tab=maintenance");
    const initial = await opening;
    expect(initial.backups).toEqual([]);
    expectCronEvidence(initial.cron, "no_complete_backup", maxAgeSeconds);
    expect(path.isAbsolute(initial.cron.directory)).toBe(true);
    expect(initial.cron.artifact_name).toBe("");
    expect(initial.cron.latest_complete_at).toBe("");
    const panels = await maintenancePanels(page);
    await expect(panels.web).toContainText("No backup records");
    await expect(panels.web.getByRole("button", { name: "Backup Now", exact: true })).toBeEnabled();
    await expectObserverEvidence(panels.observer, initial.cron);
    await expect(page.getByRole("dialog", { name: "Additional verification required" })).toHaveCount(0);

    if ((process.env.DB_BACKUP_DIR ?? "").trim() !== "") {
      throw new Error("refusing to create a Web snapshot while DB_BACKUP_DIR is set");
    }
    const posted = page.waitForResponse((response) => isApi(response, "POST", "/api/v1/system/backup-db"))
      .then((response) => readEnvelope(response, "backup create").then(parseCreatedBackup));
    let refreshed: ListedBackup[] | null = null;
    const listed = page.waitForResponse(async (response) => {
      if (!isApi(response, "GET", "/api/v1/system/backups")) return false;
      const body = await response.text();
      expect(response.status(), body).toBe(200);
      const parsed = JSON.parse(body) as { code?: unknown; data?: unknown };
      expect(parsed.code, body).toBe(200);
      const rows = parseBackupList(parsed.data);
      if (rows.length === 0) return false;
      refreshed = rows;
      return true;
    });
    await panels.web.getByRole("button", { name: "Backup Now", exact: true }).click();
    const created = await posted;
    await listed;
    if (!refreshed) throw new Error("backup list did not refresh after create");
    expect(created.filename).toMatch(/^xirang-\d{8}-\d{6}-[A-Za-z0-9_-]+\.db$/);
    expect(created.filename).not.toBe(OWN_CRON_ARTIFACT);
    expect(path.isAbsolute(created.path)).toBe(true);
    expect(created.size).toBeGreaterThan(0);
    expect(created.sha256).toMatch(/^[0-9a-f]{64}$/);
    expect(refreshed).toEqual([listedFields(created)]);
    await expect(panels.web).toContainText(created.filename);
    await expect(panels.web).not.toContainText("No backup records");
    await expect(panels.observer).not.toContainText(created.filename);
    await expect(page.getByRole("dialog", { name: "Additional verification required" })).toHaveCount(0);

    const reloadedReads = waitForMaintenanceReads(page);
    await page.reload();
    const reloaded = await reloadedReads;
    expect(reloaded.backups).toEqual([listedFields(created)]);
    expectCronEvidence(reloaded.cron, "no_complete_backup", maxAgeSeconds);
    expect(reloaded.cron.artifact_name).toBe("");
    const afterCreate = await maintenancePanels(page);
    await expect(afterCreate.web).toContainText(created.filename);
    await expect(afterCreate.observer).not.toContainText(created.filename);
    await expectObserverEvidence(afterCreate.observer, reloaded.cron);

    if (!explicit) return;
    assertAgentCronFixture(reloaded.cron.directory);
    expect(realpathSync(reloaded.cron.directory)).toBe(realpathSync(explicit));
    const remember = async (status: CronStatusName, mtime: Date, digest: string) => {
      const before = pairIdentity(published);
      const reads = waitForMaintenanceReads(page);
      await page.reload();
      const next = await reads;
      expect(pairIdentity(published)).toEqual(before);
      expect(next.backups).toEqual([listedFields(created)]);
      expectCronEvidence(next.cron, status, maxAgeSeconds);
      expect(next.cron.artifact_name).toBe(OWN_CRON_ARTIFACT);
      expect(next.cron.directory).toBe(reloaded.cron.directory);
      expect(next.cron.latest_complete_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/);
      expect(Math.abs(Date.parse(next.cron.latest_complete_at) - mtime.getTime())).toBeLessThanOrEqual(2_000);
      const cards = await maintenancePanels(page);
      await expect(cards.web).toContainText(created.filename);
      await expect(cards.web).not.toContainText(OWN_CRON_ARTIFACT);
      await expect(cards.observer).not.toContainText(created.filename);
      await expect(cards.observer).not.toContainText(digest);
      await expectObserverEvidence(cards.observer, next.cron);
    };
    const freshAt = shiftedWholeSecond(-120);
    const pair = writeCronPair(reloaded.cron.directory, freshAt);
    const [artifact, checksum] = pair.files;
    if (!artifact || !checksum) throw new Error("cron artifact pair was not created");
    published.push(artifact, checksum);
    const restamp = (mtime: Date) => {
      utimesSync(artifact, mtime, mtime);
      utimesSync(checksum, mtime, mtime);
    };
    await remember("fresh", freshAt, pair.digest);
    const staleAt = shiftedWholeSecond(-(maxAgeSeconds + 2 * 60 * 60));
    restamp(staleAt);
    await remember("stale", staleAt, pair.digest);
    const futureAt = shiftedWholeSecond(2 * 60 * 60);
    restamp(futureAt);
    await remember("clock_anomaly", futureAt, pair.digest);
  } finally {
    for (const file of new Set([...owned, ...published])) removeOwnedCronFile(file);
  }
});

test("activates TOTP with a replacement session before displaying recovery codes", async ({ page }) => {
  const terminalSmoke = terminalSmokeConfig();
  test.setTimeout(300_000);
  await login(page);
  const oldToken = await page.evaluate(() => sessionStorage.getItem("xirang-auth-token"));
  expect(oldToken).not.toBeNull();
  await page.goto("/app/tasks");
  await page.getByRole("link", { name: "Enable two-factor authentication", exact: true }).click();
  await expect(page).toHaveURL(/\/app\/settings\?tab=account$/);
  const setupResponsePromise = page.waitForResponse(response =>
    new URL(response.url()).pathname === "/api/v1/auth/2fa/setup" && response.request().method() === "POST");
  await page.getByRole("button", { name: "Enable 2FA", exact: true }).click();
  const setupResponse = await setupResponsePromise;
  expect(setupResponse.ok()).toBe(true);
  // Development StrictMode may initialize twice; use the enrollment actually
  // presented to the user, not the first response observed on the network.
  const dialog = page.getByRole("dialog", { name: "Setup Two-Factor Authentication" });
  const secretElement = dialog.locator("p.font-mono");
  await expect(secretElement).toHaveText(/^[A-Z2-7]+$/);
  const secret = (await secretElement.innerText()).trim();
  await dialog.getByRole("button", { name: "Next", exact: true }).click();
  const acceptedCodes = [-30_000, 0, 30_000].map(delta => authenticatorCode(secret, Date.now() + delta));
  let invalidCode = "000000";
  while (acceptedCodes.includes(invalidCode)) invalidCode = (Number(invalidCode) + 1).toString().padStart(6, "0");
  await dialog.getByLabel("Verification code").fill(invalidCode);
  const invalidResponsePromise = page.waitForResponse(response =>
    new URL(response.url()).pathname === "/api/v1/auth/2fa/verify" && response.request().method() === "POST");
  await dialog.getByRole("button", { name: "Verify and Enable", exact: true }).click();
  const invalidResponse = await invalidResponsePromise;
  expect(invalidResponse.status()).toBe(400);
  await expect(dialog.getByRole("alert")).toContainText("verification code is invalid");
  expect(await page.evaluate(() => sessionStorage.getItem("xirang-auth-token")) === oldToken).toBe(true);
  await dialog.getByLabel("Verification code").fill(authenticatorCode(secret));
  const verifyResponsePromise = page.waitForResponse(response =>
    new URL(response.url()).pathname === "/api/v1/auth/2fa/verify" && response.request().method() === "POST");
  await dialog.getByRole("button", { name: "Verify and Enable", exact: true }).click();
  const verifyResponse = await verifyResponsePromise;
  expect(verifyResponse.status(), JSON.stringify({
    response: verifyResponse.ok() ? "success" : await verifyResponse.json(),
    serverDate: verifyResponse.headers().date,
    clientDate: new Date().toISOString(),
  })).toBe(200);
  expect(verifyResponse.headers()["cache-control"]).toContain("no-store");
  const verification = await verifyResponse.json() as {
    data: { token: string; user: { id: number; role: string; totp_enabled: boolean }; recovery_codes: string[] };
  };
  expect(verification.data.user.totp_enabled).toBe(true);
  await expect(dialog.getByRole("button", { name: "Copy Recovery Codes", exact: true })).toBeVisible();
  const installedToken = await page.evaluate(() => sessionStorage.getItem("xirang-auth-token"));
  expect(installedToken === verification.data.token).toBe(true);
  expect(installedToken === oldToken).toBe(false);
  const oldClaims = JSON.parse(Buffer.from(oldToken!.split(".")[1], "base64url").toString()) as { jti: string; exp: number };
  const newClaims = JSON.parse(Buffer.from(installedToken!.split(".")[1], "base64url").toString()) as { jti: string; exp: number; purpose?: string };
  expect(newClaims.jti).toBe(oldClaims.jti);
  expect(newClaims.exp).toBe(oldClaims.exp);
  expect(newClaims.purpose).toBeUndefined();
  const sessions = await page.evaluate(async ({ previous, replacement }) => {
    const oldResponse = await fetch("/api/v1/me", { headers: { Authorization: `Bearer ${previous}` } });
    const newResponse = await fetch("/api/v1/me", { headers: { Authorization: `Bearer ${replacement}` } });
    const body = await newResponse.json() as { data: { user: { totp_enabled: boolean } } };
    return { oldStatus: oldResponse.status, newStatus: newResponse.status, enabled: body.data.user.totp_enabled };
  }, { previous: oldToken, replacement: installedToken });
  expect(sessions).toEqual({ oldStatus: 401, newStatus: 200, enabled: true });
  await expect(dialog.getByText(verification.data.recovery_codes[0], { exact: true })).toBeVisible();
  await dialog.getByRole("checkbox", { name: "I have saved my recovery codes", exact: true }).check();
  await dialog.getByRole("button", { name: "Finish", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Disable 2FA", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Return to operation", exact: true }).click();
  await expect(page).toHaveURL(/\/app\/tasks$/);
  await exerciseRealTerminal(page, {
    ...terminalSmoke,
    secret,
    jti: newClaims.jti,
    userId: verification.data.user.id,
  });
});

type TerminalClose = { code: number; reason: string };
type TerminalSmokeConfig = { keyPath: string; dbPath: string; port: number; username: string };
type TerminalSmoke = TerminalSmokeConfig & { secret: string; jti: string; userId: number };
type TerminalFacts = { opens: number; closes: TerminalClose[] };

function fixtureFileInside(value: unknown, field: string, runtimeReal: string): string {
  if (typeof value !== "string" || !path.isAbsolute(value)) {
    throw new Error(`isolated SSH fixture ${field} is not an absolute path`);
  }
  let info;
  try {
    info = lstatSync(value);
  } catch (error) {
    throw new Error(`isolated SSH fixture ${field} is missing (${errorCode(error)})`);
  }
  if (info.isSymbolicLink() || !info.isFile()) {
    throw new Error(`isolated SSH fixture ${field} must be a regular file`);
  }
  const resolved = realpathSync(value);
  const relative = path.relative(runtimeReal, resolved);
  if (relative === "" || relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
    throw new Error(`isolated SSH fixture ${field} escaped the runtime directory`);
  }
  return resolved;
}

function publishedTerminalFixture(runtimeDir: string): TerminalSmokeConfig {
  if (!path.isAbsolute(runtimeDir)) throw new Error("E2E_RUNTIME_DIR must be absolute");
  const scratch = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../.tmp/agent");
  let scratchInfo;
  try {
    scratchInfo = lstatSync(scratch);
  } catch (error) {
    throw new Error(`.tmp/agent is not available for the SSH fixture (${errorCode(error)})`);
  }
  if (scratchInfo.isSymbolicLink() || !scratchInfo.isDirectory()) {
    throw new Error(".tmp/agent must be a real directory");
  }
  assertNoSymlinkThrough(runtimeDir, path.dirname(scratch));
  let runtimeInfo;
  try {
    runtimeInfo = lstatSync(runtimeDir);
  } catch (error) {
    throw new Error(`E2E_RUNTIME_DIR is not an existing directory (${errorCode(error)})`);
  }
  if (runtimeInfo.isSymbolicLink() || !runtimeInfo.isDirectory()) {
    throw new Error("E2E_RUNTIME_DIR must be a real directory");
  }
  const scratchReal = realpathSync(scratch);
  const runtimeReal = realpathSync(runtimeDir);
  const relativeRuntime = path.relative(scratchReal, runtimeReal);
  if (
    relativeRuntime === ""
    || relativeRuntime === ".."
    || relativeRuntime.startsWith(`..${path.sep}`)
    || path.isAbsolute(relativeRuntime)
  ) {
    throw new Error("E2E_RUNTIME_DIR must be a subdirectory of .tmp/agent");
  }
  const fixturePath = path.join(runtimeReal, "fixture.json");
  let fixtureInfo;
  try {
    fixtureInfo = lstatSync(fixturePath);
  } catch (error) {
    throw new Error(`isolated SSH fixture is missing (${errorCode(error)})`);
  }
  if (fixtureInfo.isSymbolicLink() || !fixtureInfo.isFile()) {
    throw new Error("isolated SSH fixture must be a regular file");
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(fixturePath, "utf8"));
  } catch {
    throw new Error("isolated SSH fixture is not json");
  }
  if (!parsed || typeof parsed !== "object") throw new Error("isolated SSH fixture is not an object");
  const port = "port" in parsed ? parsed.port : undefined;
  const keyPath = "privateKeyPath" in parsed ? parsed.privateKeyPath : undefined;
  const dbPath = "sqlitePath" in parsed ? parsed.sqlitePath : undefined;
  const username = "username" in parsed ? parsed.username : undefined;
  if (typeof port !== "number" || !Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error("isolated SSH fixture port is invalid");
  }
  if (typeof username !== "string" || !/^[a-z_][a-z0-9_-]{0,31}$/.test(username)) {
    throw new Error("isolated SSH fixture username is invalid");
  }
  return {
    port,
    username,
    keyPath: fixtureFileInside(keyPath, "privateKeyPath", runtimeReal),
    dbPath: fixtureFileInside(dbPath, "sqlitePath", runtimeReal),
  };
}

function terminalSmokeConfig(): TerminalSmokeConfig {
  const runtimeDir = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  if (runtimeDir === "") throw new Error("terminal smoke env is incomplete: E2E_RUNTIME_DIR");
  return publishedTerminalFixture(runtimeDir);
}

function readPrivateKey(filePath: string): string {
  let key: string;
  try {
    key = readFileSync(filePath, "utf8");
  } catch (error) {
    const code = error && typeof error === "object" && "code" in error ? String(error.code) : "unknown";
    throw new Error(`E2E_SSH_PRIVATE_KEY_PATH could not be read (${code})`);
  }
  if (!key.includes("PRIVATE KEY") || key.includes("\0") || key.includes("ENCRYPTED")) {
    throw new Error("E2E_SSH_PRIVATE_KEY_PATH is not an unencrypted private key");
  }
  return key.endsWith("\n") ? key : `${key}\n`;
}

function dedicatedSqliteUri(dbPath: string): string {
  if (!existsSync(dbPath) || !statSync(dbPath).isFile()) {
    throw new Error("E2E_SQLITE_PATH is not an existing database file");
  }
  const uri = pathToFileURL(path.resolve(dbPath));
  uri.searchParams.set("mode", "rw");
  return uri.href;
}

function sqlite(dbPath: string, sql: string): string {
  try {
    return execFileSync("sqlite3", ["-bail", "-cmd", ".timeout 5000", dedicatedSqliteUri(dbPath), sql], {
      encoding: "utf8",
      timeout: 10_000,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : "sqlite3 failed";
    throw new Error(message.replace(/jti:[0-9a-f]+/gi, "jti:[redacted]").slice(0, 400));
  }
}

function sqlLines(output: string): string[] {
  return output.split(/\r?\n/).map((line) => line.trim()).filter((line) => line !== "");
}

function retargetNodeHost(dbPath: string, nodeId: number, nodeName: string, port: number): void {
  if (!Number.isInteger(nodeId) || nodeId < 1) throw new Error("refusing invalid node id");
  if (!/^e2e-terminal-[0-9]+$/.test(nodeName)) throw new Error("refusing unexpected node name");
  const lines = sqlLines(sqlite(dbPath, [
    `UPDATE nodes SET host = '127.0.0.1' WHERE id = ${nodeId} AND name = '${nodeName}' AND host = 'e2e-ssh.invalid';`,
    "SELECT changes();",
    `SELECT host || '|' || port FROM nodes WHERE id = ${nodeId} AND name = '${nodeName}';`,
  ].join("\n")));
  if (lines.length !== 2 || lines[0] !== "1" || lines[1] !== `127.0.0.1|${port}`) {
    throw new Error(`node host update did not retarget exactly one row: ${lines.join(" ").slice(0, 80)}`);
  }
}

function revokeDurableSession(dbPath: string, jti: string, userId: number): void {
  if (!/^[0-9a-f]{32}$/.test(jti) || !Number.isInteger(userId) || userId < 1) {
    throw new Error("refusing invalid session revocation");
  }
  const lines = sqlLines(sqlite(
    dbPath,
    `INSERT INTO token_revocations (token_hash, user_id, expires_at) VALUES ('jti:${jti}', ${userId}, '2099-01-01 00:00:00+00:00');\nSELECT changes();`,
  ));
  if (lines.length !== 1 || lines[0] !== "1") throw new Error("session revocation did not insert exactly one row");
}

function revokePriorTerminalGrants(dbPath: string, userId: number, nodeId: number): void {
  if (!Number.isInteger(userId) || userId < 1 || !Number.isInteger(nodeId) || nodeId < 1) {
    throw new Error("refusing invalid terminal grant revocation");
  }
  const lines = sqlLines(sqlite(dbPath, [
    "UPDATE credential_access_grants",
    "SET status = 'revoked', revoked_at = datetime('now'), updated_at = datetime('now')",
    `WHERE requester_user_id = ${userId} AND node_id = ${nodeId}`,
    "AND action = 'terminal.open' AND purpose = 'terminal'",
    "AND status IN ('active', 'approved');",
    "SELECT changes();",
    "SELECT COUNT(*) FROM credential_access_grants",
    `WHERE requester_user_id = ${userId} AND node_id = ${nodeId}`,
    "AND action = 'terminal.open' AND purpose = 'terminal'",
    "AND status IN ('active', 'approved');",
  ].join("\n")));
  if (lines.length !== 2 || !/^(?:0|[1-9][0-9]*)$/.test(lines[0]) || lines[1] !== "0") {
    throw new Error("prior terminal grants were not cleared");
  }
}

function returnedTerminalGrantId(body: string, userId: number, nodeId: number): number {
  let payload: unknown;
  try {
    payload = JSON.parse(body);
  } catch {
    throw new Error("terminal grant response was not json");
  }
  const data = payload && typeof payload === "object" && "data" in payload
    ? (payload as { data?: unknown }).data
    : null;
  if (!data || typeof data !== "object") throw new Error("terminal grant response has no data");
  const row = data as {
    id?: unknown;
    requester_user_id?: unknown;
    node_id?: unknown;
    action?: unknown;
    purpose?: unknown;
    status?: unknown;
  };
  if (
    typeof row.id !== "number"
    || !Number.isInteger(row.id)
    || row.id < 1
    || row.requester_user_id !== userId
    || row.node_id !== nodeId
    || row.action !== "terminal.open"
    || row.purpose !== "terminal"
    || row.status !== "active"
  ) {
    throw new Error("terminal grant response did not match the node");
  }
  return row.id;
}

function revokeReturnedTerminalGrant(dbPath: string, grantId: number, userId: number, nodeId: number): void {
  if (!Number.isInteger(grantId) || grantId < 1 || !Number.isInteger(userId) || userId < 1 || !Number.isInteger(nodeId) || nodeId < 1) {
    throw new Error("refusing invalid terminal grant revocation");
  }
  const lines = sqlLines(sqlite(dbPath, [
    "UPDATE credential_access_grants",
    "SET status = 'revoked', revoked_at = datetime('now'), updated_at = datetime('now')",
    `WHERE id = ${grantId} AND requester_user_id = ${userId} AND node_id = ${nodeId}`,
    "AND action = 'terminal.open' AND purpose = 'terminal' AND status = 'active';",
    "SELECT changes();",
    `SELECT status FROM credential_access_grants WHERE id = ${grantId};`,
  ].join("\n")));
  if (lines.length !== 2 || lines[0] !== "1" || lines[1] !== "revoked") {
    throw new Error("returned terminal grant was not revoked");
  }
}

function isolatedSshSessionParent(buffer: string, username: string): { line: string; reject: string } {
  if (!/^[a-z_][a-z0-9_-]{0,31}$/.test(username)) {
    return { line: "", reject: "unexpected ssh username" };
  }
  const session = new RegExp(`(?:^|\\s)sshd(?:-session)?:\\s+${username}@(?:pts/\\d+|notty)$`);
  const lines = buffer.split("\n").map((line) => line.trim()).filter((line) => /(?:^|\s)sshd(?:-session)?:/.test(line));
  if (lines.some((line) => (
    line.includes("[listener]")
    || line.includes("[priv]")
    || line.includes("[net]")
    || line.includes("[postauth]")
    || line.includes("sshd_config")
    || /(?:^|\s)-D(?:\s|$)/.test(line)
  ))) {
    return { line: "", reject: "parent command is not the per-connection session" };
  }
  const proven = lines.filter((line) => session.test(line));
  if (proven.length > 1) return { line: "", reject: "more than one session parent" };
  if (proven.length === 1) return { line: proven[0] ?? "", reject: "" };
  if (lines.length > 0) return { line: "", reject: "parent is not an isolated sshd session" };
  return { line: "", reject: "" };
}

function safeTerminalSummary(code: number): { text: string; status: "ended" | "failed" } {
  switch (code) {
    case 1000:
    case 1001:
      return { text: `Connection ended (${code})`, status: "ended" };
    case 1006:
      return { text: `Network connection interrupted (${code})`, status: "failed" };
    case 1007:
      return { text: `Node unavailable (${code})`, status: "failed" };
    case 1011:
      return { text: `Terminal connection failed (${code})`, status: "failed" };
    case 1008:
      return { text: `Additional verification required (${code})`, status: "failed" };
    default:
      return { text: `Terminal connection ended (${code})`, status: "ended" };
  }
}

function safeGrantText(reason: string): string {
  const detail = reason.split(":", 2)[1] ?? "";
  const safe = detail.replace(/[^\p{L}\p{N}_. -]/gu, "").trim().slice(0, 32);
  return safe ? `Terminal temporary grant required (${safe})` : "Terminal temporary grant required";
}

async function freshAuthenticatorCode(secret: string): Promise<string> {
  const remain = 30_000 - (Date.now() % 30_000);
  if (remain < 2_000) await new Promise((resolve) => setTimeout(resolve, remain + 250));
  return authenticatorCode(secret);
}

function terminalDialog(page: Page, nodeName: string): Locator {
  return page.getByRole("dialog", { name: `Web Terminal \u2014 ${nodeName}`, includeHidden: true });
}

async function installTerminalCloseCapture(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const view = window as Window & { __xirangTerminalCloseCapture?: TerminalFacts };
    if (view.__xirangTerminalCloseCapture) return;
    const opens: string[] = [];
    const closes: TerminalClose[] = [];
    view.__xirangTerminalCloseCapture = { opens, closes };
    const native = window.WebSocket;
    const Wrapped = function (url: string | URL, protocols?: string | string[]) {
      const socket = protocols === undefined ? new native(url) : new native(url, protocols);
      const href = String(url);
      if (href.includes("/api/v1/ws/terminal")) {
        opens.push(href);
        socket.addEventListener("close", (event) => {
          closes.push({ code: event.code, reason: event.reason });
        });
      }
      return socket;
    } as unknown as typeof WebSocket;
    Wrapped.prototype = native.prototype;
    Object.setPrototypeOf(Wrapped, native);
    for (const [name, value] of Object.entries({
      CONNECTING: native.CONNECTING,
      OPEN: native.OPEN,
      CLOSING: native.CLOSING,
      CLOSED: native.CLOSED,
    })) {
      Object.defineProperty(Wrapped, name, { value });
    }
    window.WebSocket = Wrapped;
  });
}

async function terminalSocketFacts(page: Page): Promise<TerminalFacts> {
  return page.evaluate(() => {
    const capture = (window as Window & { __xirangTerminalCloseCapture?: TerminalFacts }).__xirangTerminalCloseCapture;
    return { opens: capture?.opens.length ?? 0, closes: capture?.closes ?? [] };
  });
}

async function readTerminalBuffer(page: Page): Promise<string> {
  const text = await page.evaluate(() => {
    const region = document.querySelector('[aria-label="Web terminal interaction area"]');
    if (!region) return "";
    const fiberKey = Object.keys(region).find((key) => key.startsWith("__reactFiber$"));
    type Hook = { memoizedState?: { current?: { buffer?: { active?: { length: number; getLine: (index: number) => { translateToString: (trim: boolean) => string } | undefined } } } }; next?: Hook };
    type Fiber = { memoizedState?: Hook | null; return?: Fiber | null };
    const start = fiberKey ? (region as unknown as Record<string, Fiber | undefined>)[fiberKey] : undefined;
    const terminalFromHooks = (fiber: Fiber | null | undefined) => {
      let hook = fiber?.memoizedState ?? null;
      for (let count = 0; hook && count < 40; count += 1) {
        const current = hook.memoizedState?.current;
        if (current?.buffer?.active && typeof current.buffer.active.getLine === "function") return current.buffer.active;
        hook = hook.next ?? null;
      }
      return null;
    };
    let fiber = start ?? null;
    for (let depth = 0; fiber && depth < 40; depth += 1) {
      const active = terminalFromHooks(fiber);
      if (active) {
        const lines: string[] = [];
        for (let index = 0; index < active.length; index += 1) {
          lines.push(active.getLine(index)?.translateToString(true) ?? "");
        }
        return lines.join("\n");
      }
      fiber = fiber.return ?? null;
    }
    throw new Error("web terminal buffer is not mounted");
  });
  return text;
}

async function readSessionIdentity(page: Page): Promise<{ jti: string; uid: number }> {
  const identity = await page.evaluate(() => {
    const segment = sessionStorage.getItem("xirang-auth-token")?.split(".")[1];
    if (!segment) return null;
    const padded = segment.replace(/-/g, "+").replace(/_/g, "/");
    const claims = JSON.parse(atob(padded.padEnd(Math.ceil(padded.length / 4) * 4, "="))) as { jti?: unknown; uid?: unknown };
    return {
      jti: typeof claims.jti === "string" ? claims.jti : "",
      uid: typeof claims.uid === "number" ? claims.uid : 0,
    };
  });
  if (!identity || !/^[0-9a-f]{32}$/.test(identity.jti) || !Number.isInteger(identity.uid) || identity.uid < 1) {
    throw new Error("browser session is missing a durable id");
  }
  return identity;
}

async function submitFreshProof(page: Page, secret: string, stepUps: { count: number }): Promise<void> {
  const dialog = page.getByRole("dialog", { name: "Additional verification required" });
  await expect(dialog).toBeVisible({ timeout: 15_000 });
  const before = stepUps.count;
  await dialog.locator("#step-up-code").fill(await freshAuthenticatorCode(secret));
  await dialog.getByRole("button", { name: "Verify", exact: true }).click();
  await expect.poll(() => stepUps.count, { timeout: 15_000 }).toBe(before + 1);
  await expect(dialog).toBeHidden();
}

async function typeShell(page: Page, dialog: Locator, command: string): Promise<void> {
  const textarea = dialog.locator("textarea.xterm-helper-textarea");
  await textarea.evaluate((element) => {
    if (!(element instanceof HTMLTextAreaElement)) throw new Error("terminal input is not a textarea");
    element.focus();
  });
  await expect(textarea).toBeFocused();
  await page.keyboard.type(command, { delay: 20 });
  await page.keyboard.press("Enter");
}

async function nextTerminalClose(page: Page, previous: number): Promise<TerminalClose> {
  await expect.poll(async () => (await terminalSocketFacts(page)).closes.length, { timeout: 20_000 }).toBe(previous + 1);
  const close = (await terminalSocketFacts(page)).closes[previous];
  if (!close || !Number.isInteger(close.code)) throw new Error("terminal close event did not include a code");
  return close;
}

async function nudgeVisibility(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
    const proto = Document.prototype;
    const visibility = Object.getOwnPropertyDescriptor(proto, "visibilityState");
    const hidden = Object.getOwnPropertyDescriptor(proto, "hidden");
    if (!visibility?.configurable || !hidden?.configurable) {
      document.dispatchEvent(new Event("visibilitychange"));
      await wait(3_200);
      return;
    }
    let state: DocumentVisibilityState = "hidden";
    try {
      Object.defineProperty(proto, "visibilityState", { configurable: true, get: () => state });
      Object.defineProperty(proto, "hidden", { configurable: true, get: () => state === "hidden" });
      document.dispatchEvent(new Event("visibilitychange"));
      await wait(300);
      state = "visible";
      document.dispatchEvent(new Event("visibilitychange"));
      await wait(2_900);
    } finally {
      Object.defineProperty(proto, "visibilityState", visibility);
      Object.defineProperty(proto, "hidden", hidden);
    }
  });
}

async function expectRetainedSafeClose(page: Page, dialog: Locator, close: TerminalClose): Promise<void> {
  const safe = safeTerminalSummary(close.code);
  const summary = dialog.locator("#terminal-connection-summary");
  await expect(summary).toHaveText(safe.text);
  await expect(dialog.locator("[data-connection-status]")).toHaveAttribute("data-connection-status", safe.status);
  await expect(summary).not.toContainText(/normal exit/i);
  await expect(dialog.getByRole("button", { name: "Reconnect", exact: true })).toBeVisible();
  await expect(dialog.locator("#terminal-reconnect-hint")).toContainText("new SSH session");
  const buffer = await readTerminalBuffer(page);
  expect(buffer).not.toContain("CREDENTIAL_GRANT_REQUIRED");
  expect(buffer).not.toContain("PRIVATE KEY");
  if (close.reason && !safe.text.includes(close.reason)) {
    await expect(summary).not.toContainText(close.reason);
    expect(buffer).not.toContain(close.reason);
  }
}

async function expectNoAutomaticSockets(
  page: Page,
  dialog: Locator,
  opens: number,
  playwrightOpens: number,
  sockets: WebSocket[],
  summary: string,
): Promise<void> {
  await nudgeVisibility(page);
  const facts = await terminalSocketFacts(page);
  expect(facts.opens).toBe(opens);
  expect(sockets.length).toBe(playwrightOpens);
  expect(sockets.filter((socket) => !socket.isClosed()).length).toBe(0);
  await expect(dialog.locator("#terminal-connection-summary")).toHaveText(summary);
  const status = dialog.locator("[data-connection-status]");
  await expect(status).not.toHaveAttribute("data-connection-status", "connecting");
  await expect(status).not.toHaveAttribute("data-connection-status", "connected");
}

async function exitShell(page: Page, dialog: Locator, sockets: WebSocket[], command: string): Promise<void> {
  const before = await terminalSocketFacts(page);
  const playwrightBefore = sockets.length;
  await typeShell(page, dialog, command);
  const close = await nextTerminalClose(page, before.closes.length);
  test.info().annotations.push({ type: "terminal-close", description: `${command}=${close.code}` });
  await expectRetainedSafeClose(page, dialog, close);
  await expectNoAutomaticSockets(page, dialog, before.opens, playwrightBefore, sockets, safeTerminalSummary(close.code).text);
}

async function admitTerminal(
  page: Page,
  secret: string,
  dialog: Locator,
  stepUps: { count: number },
  sockets: WebSocket[],
): Promise<void> {
  await expect(dialog).toBeVisible();
  const before = await terminalSocketFacts(page);
  const playwrightBefore = sockets.length;
  const proofsBefore = stepUps.count;
  await submitFreshProof(page, secret, stepUps);
  const grant = page.getByRole("dialog", { name: "Terminal temporary grant required" });
  const connected = dialog.locator('[data-connection-status="connected"]');
  await expect(grant.or(connected)).toBeVisible({ timeout: 20_000 });
  if (!(await grant.isVisible())) {
    expect((await terminalSocketFacts(page)).opens).toBe(before.opens + 1);
    expect((await terminalSocketFacts(page)).closes.length).toBe(before.closes.length);
    await expect.poll(() => sockets.length).toBe(playwrightBefore + 1);
    await expect(connected).toBeVisible();
    return;
  }
  await expect.poll(async () => (await terminalSocketFacts(page)).closes.length, { timeout: 10_000 }).toBe(before.closes.length + 1);
  const facts = await terminalSocketFacts(page);
  expect(facts.opens).toBe(before.opens + 1);
  await expect.poll(() => sockets.length).toBe(playwrightBefore + 1);
  const grantClose = facts.closes[facts.closes.length - 1];
  expect(grantClose?.code).toBe(1008);
  expect(grantClose?.reason.startsWith("CREDENTIAL_GRANT_REQUIRED:")).toBe(true);
  await expect(dialog.locator("#terminal-connection-summary")).toHaveCount(0);
  await expect(grant.getByRole("status")).toHaveText(safeGrantText(grantClose?.reason ?? ""));
  await expect(grant).not.toContainText("CREDENTIAL_GRANT_REQUIRED");
  await grant.locator("#terminal-grant-reason").fill("lifecycle terminal smoke");
  await page.waitForTimeout(1_000);
  expect((await terminalSocketFacts(page)).opens).toBe(facts.opens);
  expect(stepUps.count).toBe(proofsBefore + 1);
  await grant.getByRole("button", { name: "Request and retry", exact: true }).click();
  const challenge = page.getByRole("dialog", { name: "Additional verification required" });
  await expect(connected.or(challenge)).toBeVisible({ timeout: 20_000 });
  if (await challenge.isVisible()) {
    await submitFreshProof(page, secret, stepUps);
  } else if (stepUps.count !== proofsBefore + 1) {
    throw new Error("grant handoff requested another authenticator code without a server challenge");
  }
  await expect(grant).toBeHidden();
  await expect(connected).toBeVisible({ timeout: 20_000 });
  expect((await terminalSocketFacts(page)).opens).toBe(before.opens + 2);
  await expect.poll(() => sockets.length).toBe(playwrightBefore + 2);
}

async function pauseApiFetches(page: Page): Promise<void> {
  await page.evaluate(() => {
    const view = window as Window & { __xirangFetchPaused?: boolean };
    if (view.__xirangFetchPaused) return;
    view.__xirangFetchPaused = true;
    window.fetch = (() => new Promise(() => {})) as typeof window.fetch;
  });
}

async function exerciseRealTerminal(page: Page, smoke: TerminalSmoke): Promise<void> {
  const terminalUser = smoke.username;
  const stepUps = { count: 0 };
  const sockets: WebSocket[] = [];
  page.on("response", (response) => {
    if (!response.ok() || response.request().method() !== "POST") return;
    if (new URL(response.url()).pathname === "/api/v1/auth/step-up") stepUps.count += 1;
  });
  page.on("websocket", (socket) => {
    if (socket.url().includes("/api/v1/ws/terminal")) sockets.push(socket);
  });

  const nodeName = `e2e-terminal-${Date.now()}`;
  let nodeId = 0;
  await test.step("create the node through the API, then retarget only that host", async () => {
    const privateKey = readPrivateKey(smoke.keyPath);
    const created = await page.evaluate(async (request: { name: string; port: number; privateKey: string; username: string }) => {
      const token = sessionStorage.getItem("xirang-auth-token");
      const response = await fetch("/api/v1/nodes", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${token ?? ""}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          name: request.name,
          host: "e2e-ssh.invalid",
          port: request.port,
          username: request.username,
          auth_type: "key",
          ssh_key_id: null,
          private_key: request.privateKey,
          base_path: "/",
          tags: "",
          use_sudo: false,
        }),
      });
      let id: number | null = null;
      let host: string | null = null;
      let message = "";
      try {
        const payload = await response.json() as { message?: string; data?: { id?: number; host?: string } };
        message = typeof payload.message === "string" ? payload.message : "";
        id = typeof payload.data?.id === "number" ? payload.data.id : null;
        host = typeof payload.data?.host === "string" ? payload.data.host : null;
      } catch {
        message = "unreadable response";
      }
      return { status: response.status, id, host, message };
    }, { name: nodeName, port: smoke.port, privateKey, username: terminalUser });
    nodeId = created.id ?? 0;
    if (created.status !== 201 || created.host !== "e2e-ssh.invalid" || !Number.isInteger(nodeId) || nodeId < 1) {
      const detail = created.message.includes("PRIVATE") ? "rejected" : created.message.slice(0, 160);
      throw new Error(`node create failed (${created.status}): ${detail}`);
    }
    retargetNodeHost(smoke.dbPath, nodeId, nodeName, smoke.port);
  });

  await installTerminalCloseCapture(page);
  await page.goto("/app/nodes");
  const openTerminal = page.locator(".md\\:grid").getByRole("button", { name: `Open web terminal for node ${nodeName}`, exact: true });
  await expect(openTerminal).toBeVisible({ timeout: 15_000 });
  const marker = `P1MARKER${Date.now()}`;

  await test.step("open a shell with one proof and grant handoff", async () => {
    await openTerminal.click();
    const dialog = terminalDialog(page, nodeName);
    await admitTerminal(page, smoke.secret, dialog, stepUps, sockets);
    await typeShell(page, dialog, `echo ${marker}`);
    await expect.poll(async () => readTerminalBuffer(page), { timeout: 20_000 }).toContain(marker);
    await exitShell(page, dialog, sockets, "exit");
  });

  await test.step("reconnect with a fresh proof and keep the previous output", async () => {
    const dialog = terminalDialog(page, nodeName);
    await dialog.getByRole("button", { name: "Reconnect", exact: true }).click();
    await admitTerminal(page, smoke.secret, dialog, stepUps, sockets);
    await expect.poll(async () => {
      const text = await readTerminalBuffer(page);
      return text.includes(marker) && text.includes("New SSH session");
    }, { timeout: 20_000 }).toBe(true);
    await exitShell(page, dialog, sockets, "exit 7");
  });

  await test.step("reconnect after the transport dies, then close the parent dialog", async () => {
    const dialog = terminalDialog(page, nodeName);
    await dialog.getByRole("button", { name: "Reconnect", exact: true }).click();
    await admitTerminal(page, smoke.secret, dialog, stepUps, sockets);
    await typeShell(page, dialog, 'ps -o comm=,args= -p "$PPID"');
    let proof = { line: "", reject: "" };
    await expect.poll(async () => {
      proof = isolatedSshSessionParent(await readTerminalBuffer(page), terminalUser);
      if (proof.reject) {
        throw new Error(`shell parent is not the isolated sshd session (${proof.reject}); refusing to signal it`);
      }
      return proof.line;
    }, { timeout: 20_000, message: "shell parent was not shown; refusing to signal it" }).not.toBe("");
    if (!proof.line || proof.line.includes("[listener]") || proof.line.includes(" -D ") || proof.line.includes("[postauth]")) {
      throw new Error("shell parent is not the isolated sshd session; refusing to signal it");
    }
    const before = await terminalSocketFacts(page);
    const playwrightBefore = sockets.length;
    // Parent is the per-connection sshd-session (user@pts or notty), not the listener.
    await typeShell(page, dialog, 'kill -KILL "$PPID"');
    const close = await nextTerminalClose(page, before.closes.length);
    test.info().annotations.push({ type: "terminal-close", description: `transport=${close.code}` });
    await expectRetainedSafeClose(page, dialog, close);
    expect(await readTerminalBuffer(page)).toContain(proof.line);
    await expectNoAutomaticSockets(page, dialog, before.opens, playwrightBefore, sockets, safeTerminalSummary(close.code).text);

    await dialog.getByRole("button", { name: "Reconnect", exact: true }).click();
    await admitTerminal(page, smoke.secret, dialog, stepUps, sockets);
    const reopened = `P1REOPEN${Date.now()}`;
    await typeShell(page, dialog, `echo ${reopened}`);
    await expect.poll(async () => readTerminalBuffer(page), { timeout: 20_000 }).toContain(reopened);
    const live = sockets.filter((socket) => !socket.isClosed());
    expect(live).toHaveLength(1);
    const opens = (await terminalSocketFacts(page)).opens;
    const closes = (await terminalSocketFacts(page)).closes.length;
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await expect(dialog).toBeHidden();
    await expect.poll(async () => (await terminalSocketFacts(page)).closes.length, { timeout: 10_000 }).toBe(closes + 1);
    expect((await terminalSocketFacts(page)).opens).toBe(opens);
    expect(sockets.filter((socket) => !socket.isClosed()).length).toBe(0);
    await page.waitForTimeout(1_000);
    expect((await terminalSocketFacts(page)).opens).toBe(opens);
    expect((await terminalSocketFacts(page)).closes.length).toBe(closes + 1);
  });

  await test.step("reject one revoked grant during handoff", async () => {
    if (!Number.isInteger(nodeId) || nodeId < 1) throw new Error("refusing invalid terminal grant revocation");
    revokePriorTerminalGrants(smoke.dbPath, smoke.userId, nodeId);
    await openTerminal.click();
    const dialog = terminalDialog(page, nodeName);
    await expect(dialog).toBeVisible();
    const admission = await terminalSocketFacts(page);
    const admissionSockets = sockets.length;
    await submitFreshProof(page, smoke.secret, stepUps);
    const grant = page.getByRole("dialog", { name: "Terminal temporary grant required" });
    await expect(grant).toBeVisible({ timeout: 20_000 });
    await expect.poll(async () => (await terminalSocketFacts(page)).closes.length, { timeout: 10_000 }).toBe(admission.closes.length + 1);
    const facts = await terminalSocketFacts(page);
    expect(facts.opens).toBe(admission.opens + 1);
    await expect.poll(() => sockets.length).toBe(admissionSockets + 1);
    const admissionClose = facts.closes[facts.closes.length - 1];
    expect(admissionClose?.code).toBe(1008);
    expect(admissionClose?.reason.startsWith("CREDENTIAL_GRANT_REQUIRED:")).toBe(true);
    expect(sockets.filter((socket) => !socket.isClosed()).length).toBe(0);
    await expect(grant.getByRole("status")).toHaveText(safeGrantText(admissionClose?.reason ?? ""));
    await expect(grant).not.toContainText("CREDENTIAL_GRANT_REQUIRED");

    const handoffBefore = await terminalSocketFacts(page);
    const handoffSockets = sockets.length;
    const created = { count: 0, posts: 0 };
    const grantPath = "**/api/v1/credential-access-grants/terminal";
    const intercept = async (route: Route) => {
      if (route.request().method() !== "POST") {
        await route.continue();
        return;
      }
      created.posts += 1;
      let fulfilled = false;
      try {
        const response = await route.fetch();
        const body = await response.text();
        if (response.status() === 201) {
          const grantId = returnedTerminalGrantId(body, smoke.userId, nodeId);
          revokeReturnedTerminalGrant(smoke.dbPath, grantId, smoke.userId, nodeId);
          created.count += 1;
        }
        await route.fulfill({ response, body });
        fulfilled = true;
      } catch (error) {
        if (!fulfilled) await route.abort();
        throw error;
      }
    };
    await page.route(grantPath, intercept);
    try {
      await grant.locator("#terminal-grant-reason").fill("lifecycle terminal smoke");
      await page.waitForTimeout(1_000);
      expect((await terminalSocketFacts(page)).opens).toBe(handoffBefore.opens);
      await grant.getByRole("button", { name: "Request and retry", exact: true }).click();
      const challenge = page.getByRole("dialog", { name: "Additional verification required" });
      let proofStarted = false;
      await expect.poll(async () => {
        if (created.count >= 1) return created.count;
        if (!proofStarted && await challenge.isVisible()) {
          proofStarted = true;
          await submitFreshProof(page, smoke.secret, stepUps);
        }
        return created.count;
      }, { timeout: 25_000 }).toBe(1);
      const postsAtHandoff = created.posts;
      const close = await nextTerminalClose(page, handoffBefore.closes.length);
      expect((await terminalSocketFacts(page)).opens).toBe(handoffBefore.opens + 1);
      expect(close.code).toBe(1008);
      expect(close.reason).toBe("CREDENTIAL_GRANT_REQUIRED:revoked");
      test.info().annotations.push({ type: "terminal-close", description: `grant-revoked=${close.code}` });
      const safe = safeGrantText(close.reason);
      await expect(grant.getByRole("status")).toHaveText(safe);
      await expect(grant).not.toContainText("CREDENTIAL_GRANT_REQUIRED");
      await expect(dialog.locator("#terminal-connection-summary")).toHaveCount(0);
      const buffer = await readTerminalBuffer(page);
      expect(buffer).not.toContain("CREDENTIAL_GRANT_REQUIRED");
      expect(buffer).toContain(safe);
      await nudgeVisibility(page);
      const after = await terminalSocketFacts(page);
      expect(after.opens).toBe(handoffBefore.opens + 1);
      expect(after.closes.length).toBe(handoffBefore.closes.length + 1);
      expect(created.count).toBe(1);
      expect(created.posts).toBe(postsAtHandoff);
      expect(created.posts).toBeLessThanOrEqual(2);
      expect(sockets.length).toBe(handoffSockets + 1);
      expect(sockets.filter((socket) => !socket.isClosed()).length).toBe(0);
      await expect(dialog.locator("[data-connection-status]")).toHaveAttribute("data-connection-status", "ended");
      await expect(dialog.locator("[data-connection-status]")).not.toHaveAttribute("data-connection-status", "connecting");
      await expect(dialog.locator("[data-connection-status]")).not.toHaveAttribute("data-connection-status", "connected");
    } finally {
      await page.unroute(grantPath, intercept);
    }
    await grant.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(grant).toBeHidden();
    const opens = (await terminalSocketFacts(page)).opens;
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await expect(dialog).toBeHidden();
    expect((await terminalSocketFacts(page)).opens).toBe(opens);
  });

  await test.step("revoke the durable session while a shell is connected", async () => {
    await openTerminal.click();
    const dialog = terminalDialog(page, nodeName);
    await admitTerminal(page, smoke.secret, dialog, stepUps, sockets);
    await pauseApiFetches(page);
    const live = `P1LIVE${Date.now()}`;
    await typeShell(page, dialog, `echo ${live}`);
    await expect.poll(async () => readTerminalBuffer(page), { timeout: 20_000 }).toContain(live);
    const identity = await readSessionIdentity(page);
    if (identity.jti !== smoke.jti || identity.uid !== smoke.userId) {
      throw new Error("browser session does not match the activated user");
    }
    const before = await terminalSocketFacts(page);
    const playwrightBefore = sockets.length;
    revokeDurableSession(smoke.dbPath, identity.jti, identity.uid);
    const close = await nextTerminalClose(page, before.closes.length);
    test.info().annotations.push({ type: "terminal-close", description: `revocation=${close.code}` });
    await expectRetainedSafeClose(page, dialog, close);
    await expectNoAutomaticSockets(page, dialog, before.opens, playwrightBefore, sockets, safeTerminalSummary(close.code).text);
  });
}
