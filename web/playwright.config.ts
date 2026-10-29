import { defineConfig, devices } from "@playwright/test";
import { evidenceFileVariable } from "./e2e/run-evidence-reporter";

export default defineConfig({
  testDir: "./e2e",
  testIgnore: [/real-backend-smoke\.spec\.ts/, /lifecycle-p1\.spec\.ts/, /config-name-mapping\.spec\.ts/, /ssh-key-rotation\.spec\.ts/, /system-backup-download\.spec\.ts/, /account-recovery\.spec\.ts/, /escalation-preview\.spec\.ts/, /\/walkthrough\//, /\/surface-states\//],
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  // Evidence runs (scripts/run-e2e-evidence.mjs) keep only safe identity and
  // status output; human reporters print raw errors and stay for direct runs.
  reporter: process.env[evidenceFileVariable] ? "./e2e/run-evidence-reporter.ts" : process.env.CI ? "github" : "list",
  use: {
    baseURL: "http://127.0.0.1:4177",
    trace: "on-first-retry",
  },
  webServer: {
    command: "npx vite --host 127.0.0.1 --port 4177 --strictPort",
    url: "http://127.0.0.1:4177/login",
    reuseExistingServer: !process.env.CI && !process.env[evidenceFileVariable],
    timeout: 120_000,
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "firefox", use: { ...devices["Desktop Firefox"] } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
});
