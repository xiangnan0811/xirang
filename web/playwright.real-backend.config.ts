import { randomBytes } from "node:crypto";
import { lstatSync, mkdirSync, mkdtempSync, realpathSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, devices } from "@playwright/test";

const configDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(configDir, "..");
const scratchBoundary = path.join(repositoryRoot, ".tmp");
const agentScratch = path.join(scratchBoundary, "agent");
const lifecycleRoot = path.join(agentScratch, "lifecycle-p1-e2e");

function errorCode(error: unknown): string {
  return error && typeof error === "object" && "code" in error ? String(error.code) : "";
}

function ensureRealDirectory(directory: string): void {
  let info;
  try {
    info = lstatSync(directory);
  } catch (error) {
    if (errorCode(error) !== "ENOENT") throw error;
    mkdirSync(directory, { mode: 0o700 });
    info = lstatSync(directory);
  }
  if (info.isSymbolicLink() || !info.isDirectory()) {
    throw new Error(`${directory} must be a real directory for the real-backend runtime`);
  }
}

function assertNoSymlinkThrough(leaf: string, boundary: string): void {
  let current = path.resolve(leaf);
  const stop = path.resolve(boundary);
  for (;;) {
    const info = lstatSync(current);
    if (info.isSymbolicLink()) throw new Error(`${current} must not be a symlink`);
    if (current === stop) return;
    const parent = path.dirname(current);
    if (parent === current) throw new Error(`${leaf} is outside ${boundary}`);
    current = parent;
  }
}

function escaped(relative: string): boolean {
  return relative === "" || relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative);
}

function assertRuntimeDirectory(directory: string): string {
  const lexical = path.resolve(directory);
  assertNoSymlinkThrough(lexical, scratchBoundary);
  if (!lstatSync(lexical).isDirectory()) throw new Error("real-backend runtime must be a directory");
  const parent = path.dirname(lexical);
  if (path.basename(parent) !== "lifecycle-p1-e2e" || !/^runtime\.[A-Za-z0-9]+$/.test(path.basename(lexical))) {
    throw new Error("real-backend runtime is not a fresh lifecycle directory");
  }
  const scratchReal = realpathSync(agentScratch);
  if (path.dirname(realpathSync(parent)) !== scratchReal || escaped(path.relative(scratchReal, realpathSync(lexical)))) {
    throw new Error("real-backend runtime escaped .tmp/agent");
  }
  return lexical;
}

for (const directory of [scratchBoundary, agentScratch, lifecycleRoot]) ensureRealDirectory(directory);
assertNoSymlinkThrough(lifecycleRoot, scratchBoundary);

// A fresh empty directory is created once per invocation. Workers reuse it only
// when this process already marked that same run; nothing is written inside it.
const sameRunMark = (process.env.E2E_RUNTIME_SAME_RUN ?? "").trim();
const inheritedRuntime = (process.env.E2E_RUNTIME_DIR ?? "").trim();
const sameRun = /^[0-9a-f]{32}$/.test(sameRunMark);
let runtimeDir = "";
if (sameRun) {
  if (inheritedRuntime === "") throw new Error("same-run runtime is missing E2E_RUNTIME_DIR");
  runtimeDir = assertRuntimeDirectory(inheritedRuntime);
} else if (sameRunMark !== "") {
  throw new Error("refusing an inherited runtime that is not marked for this run");
} else {
  runtimeDir = assertRuntimeDirectory(mkdtempSync(path.join(lifecycleRoot, "runtime.")));
}
const sameRunToken = sameRun ? sameRunMark : randomBytes(16).toString("hex");
process.env.E2E_RUNTIME_DIR = runtimeDir;
process.env.E2E_RUNTIME_SAME_RUN = sameRunToken;
for (const name of ["E2E_SQLITE_PATH", "E2E_CRON_DB_BACKUP_DIR", "E2E_SSH_KNOWN_HOSTS_PATH", "E2E_SSH_PRIVATE_KEY_PATH", "E2E_SSH_PORT", "E2E_SSH_USERNAME"]) {
  delete process.env[name];
}

const backendPort = process.env.E2E_BACKEND_PORT ?? "18080";
const frontendPort = process.env.E2E_VITE_PORT ?? "4178";
const backendURL = `http://127.0.0.1:${backendPort}`;
const frontendURL = `http://127.0.0.1:${frontendPort}`;
const chromium = { ...devices["Desktop Chrome"] };

export default defineConfig({
  testDir: "./e2e",
  testMatch: /(real-backend-smoke|lifecycle-p1|config-name-mapping|ssh-key-rotation)\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: frontendURL,
    trace: "on-first-retry",
  },
  webServer: [
    {
      command: "bash scripts/e2e-ssh.sh",
      cwd: repositoryRoot,
      url: `${backendURL}/readyz`,
      reuseExistingServer: false,
      timeout: 120_000,
      env: {
        E2E_RUNTIME_DIR: runtimeDir,
        E2E_RUNTIME_SAME_RUN: sameRunToken,
        E2E_BACKEND_PORT: backendPort,
        E2E_VITE_PORT: frontendPort,
      },
    },
    {
      command: `npx vite --host 127.0.0.1 --port ${frontendPort} --strictPort`,
      cwd: configDir,
      url: `${frontendURL}/login`,
      reuseExistingServer: false,
      timeout: 120_000,
      env: {
        VITE_API_BASE_URL: "/api/v1",
        VITE_PROXY_TARGET: backendURL,
        // Keep the development-only direct fallback unusable in this test. A
        // successful smoke must therefore traverse Vite's /api proxy.
        VITE_DEV_API_DIRECT_URL: "http://127.0.0.1:1/api/v1",
        VITE_ENABLE_DEMO_MODE: "false",
      },
    },
  ],
  projects: [
    {
      name: "chromium-smoke",
      testMatch: /real-backend-smoke\.spec\.ts/,
      use: chromium,
    },
    {
      name: "chromium",
      testMatch: /(lifecycle-p1|config-name-mapping|ssh-key-rotation)\.spec\.ts/,
      dependencies: ["chromium-smoke"],
      use: chromium,
    },
  ],
});