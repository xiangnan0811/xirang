import { lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync, writeSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, devices } from "@playwright/test";

// Disable Playwright's failure-time ARIA DOM capture as it includes live codes.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = "1";
if (process.env.DEBUG || process.env.PWDEBUG) {
  throw new Error("Recovery tests require DEBUG and PWDEBUG unset to prevent credential logs");
}

const web = path.dirname(fileURLToPath(import.meta.url));
const repository = path.dirname(web);
const scratch = path.join(repository, ".tmp/agent");
const runVariable = "R19_RECOVERY_PLAYWRIGHT_RUN";
const ownerPrefix = "exclusive recovery playwright run ";

// The runner process creates the run directory and records its own pid in the
// owner file. Workers re-evaluate this config and may only adopt a directory
// whose owner is their parent runner, so stale or foreign environment values
// can never make two runs share output.
function ownedBy(directory: string, pid: number): boolean {
  try {
    const info = lstatSync(directory);
    const owner = lstatSync(path.join(directory, "owner"));
    return path.dirname(directory) === scratch && info.isDirectory() && info.uid === process.getuid?.()
      && realpathSync(directory) === path.join(realpathSync(scratch), path.basename(directory))
      && owner.isFile() && readFileSync(path.join(directory, "owner"), "utf8") === `${ownerPrefix}${pid}\n`;
  } catch {
    return false;
  }
}

function createRunDirectory(): string {
  for (const directory of [path.join(repository, ".tmp"), scratch]) {
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    const info = lstatSync(directory);
    if (info.isSymbolicLink() || !info.isDirectory()) throw new Error("Unsafe recovery scratch directory");
  }
  const directory = mkdtempSync(path.join(scratch, "r19-playwright-"));
  if (path.dirname(realpathSync(directory)) !== realpathSync(scratch)) throw new Error("Recovery scratch escaped boundary");
  writeFileSync(path.join(directory, "owner"), `${ownerPrefix}${process.pid}\n`, { mode: 0o600, flag: "wx" });
  return directory;
}

// Runs at runner exit, after every reporter (including .last-run.json) wrote.
// Success removes the whole owned run. Failure keeps redacted evidence from
// failed tests; a run with only .last-run.json leaves nothing behind.
function finishRun(directory: string, code: number): void {
  if (!ownedBy(directory, process.pid)) return;
  const results = path.join(directory, "results");
  let evidence = false;
  try {
    evidence = readdirSync(results, { recursive: true, withFileTypes: true })
      .some(entry => entry.isFile() && entry.name !== ".last-run.json");
  } catch { /* Playwright wrote no output. */ }
  if (code !== 0 && evidence) {
    writeSync(2, `Recovery redacted failure evidence preserved: ${path.relative(repository, results)}\n`);
    return;
  }
  rmSync(directory, { recursive: true, force: true });
}

function runDirectory(): string {
  const worker = process.env.TEST_WORKER_INDEX !== undefined;
  const inherited = process.env[runVariable];
  if (inherited && ownedBy(inherited, worker ? process.ppid : process.pid)) return inherited;
  if (worker) throw new Error("Recovery worker lacks its runner-owned output directory");
  const directory = createRunDirectory();
  process.env[runVariable] = directory;
  process.on("exit", code => finishRun(directory, code));
  return directory;
}

// Run from web/: env -u NODE_ENV npx playwright test
// --config=playwright.account-recovery.config.ts --project=chromium
// Optional bounded manual inspection, same fixture and automatic teardown:
// R19_RECOVERY_INSPECT_MS=120000 env -u NODE_ENV npx playwright test
// --config=playwright.account-recovery.config.ts --project=chromium --headed
// -g 'factor-only admin en'
// Each test owns fresh runtime data, binaries, loopback ports and child processes.
// Playwright output goes to an exclusive .tmp/agent/r19-playwright-* run, never web/test-results.
// Never enable trace/video/automatic screenshots: login and QR contain secrets.
export default defineConfig({
  testDir: "./e2e",
  testMatch: /account-recovery\.spec\.ts/,
  outputDir: path.join(runDirectory(), "results"),
  preserveOutput: "failures-only",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 180_000,
  expect: { timeout: 15_000 },
  forbidOnly: Boolean(process.env.CI),
  reporter: "./e2e/account-recovery-reporter.ts",
  use: { trace: "off", video: "off", screenshot: "off" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
