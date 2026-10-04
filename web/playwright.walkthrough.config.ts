import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e/walkthrough",
  fullyParallel: true,
  workers: 4,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  expect: { timeout: 20_000 },
  use: {
    baseURL: "http://127.0.0.1:4177",
    trace: "on-first-retry",
    reducedMotion: "reduce",
  },
  webServer: {
    command: "npx vite --host 127.0.0.1 --port 4177 --strictPort",
    env: { VITE_ENABLE_DEMO_MODE: "false" },
    url: "http://127.0.0.1:4177/login",
    reuseExistingServer: false,
    timeout: 120_000,
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
});
