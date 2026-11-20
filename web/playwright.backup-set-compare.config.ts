import { readFileSync, lstatSync, realpathSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, devices } from "@playwright/test";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const scratch = path.join(root, ".tmp", "agent");
const requested = process.env.XIRANG_COMPARE_BROWSER_FIXTURE_DIR;
if (!requested) throw new Error("XIRANG_COMPARE_BROWSER_FIXTURE_DIR is required");
const directory = realpathSync(requested);
const relative = path.relative(realpathSync(scratch), directory);
if (!relative || relative.startsWith(`..${path.sep}`) || relative === ".." || path.isAbsolute(relative)) {
  throw new Error("fixture directory must be a private child of .tmp/agent");
}
const info = lstatSync(directory);
if (!info.isDirectory() || (info.mode & 0o077) !== 0) throw new Error("fixture directory must be private");
const descriptor: unknown = JSON.parse(readFileSync(path.join(directory, "descriptor.json"), "utf8"));
if (typeof descriptor !== "object" || descriptor === null || !("url" in descriptor) || typeof descriptor.url !== "string") {
  throw new Error("invalid browser fixture descriptor");
}
const backend = new URL(descriptor.url);
if (backend.protocol !== "http:" || backend.hostname !== "127.0.0.1" || !backend.port || backend.pathname !== "/") {
  throw new Error("fixture backend must be a loopback HTTP listener");
}
const port = process.env.XIRANG_COMPARE_VITE_PORT ?? "4179";
if (!/^[1-9][0-9]{0,4}$/.test(port) || Number(port) > 65535) throw new Error("invalid Vite port");

export default defineConfig({
  testDir: "./e2e",
  testMatch: /backup-set-compare\.browser\.ts/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  outputDir: path.join(directory, "playwright-results"),
  reporter: "list",
  use: { ...devices["Desktop Chrome"], baseURL: `http://127.0.0.1:${port}`, trace: "off", screenshot: "off", video: "off" },
  webServer: {
    command: `npx vite --config vite.backup-set-compare.config.ts --host 127.0.0.1 --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}/login`,
    env: { VITE_PROXY_TARGET: backend.origin },
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
