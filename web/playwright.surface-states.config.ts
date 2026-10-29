import path from "node:path";
import { defineConfig, devices } from "@playwright/test";

// Docker volume and task run history browser states. The suite file avoids the
// *.spec.ts wildcard so neither the default config nor the 464-scenario
// walkthrough collects it; only this explicit testMatch selects it. Under
// scripts/run-e2e-evidence.mjs the per-attempt evidence lands beside the run
// events in <run directory>/output for compile-walkthrough-evidence.mjs.
const evidenceFile = process.env.XIRANG_E2E_EVIDENCE_FILE;

export default defineConfig({
  testDir: "./e2e/surface-states",
  testMatch: ["surface-states.browser.ts"],
  outputDir: evidenceFile ? path.join(path.dirname(evidenceFile), "output") : "test-results/surface-states",
  fullyParallel: true,
  workers: 4,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  reporter: evidenceFile
    ? [["./e2e/run-evidence-reporter.ts"]]
    : [[process.env.CI ? "github" : "list"]],
  expect: { timeout: 20_000 },
  use: {
    baseURL: "http://127.0.0.1:4178",
    trace: "retain-on-failure",
    reducedMotion: "reduce",
  },
  webServer: {
    command: "npx vite --host 127.0.0.1 --port 4178 --strictPort",
    env: { VITE_ENABLE_DEMO_MODE: "false" },
    url: "http://127.0.0.1:4178/login",
    reuseExistingServer: false,
    timeout: 120_000,
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
});
