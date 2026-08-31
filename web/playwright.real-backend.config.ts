import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, devices } from "@playwright/test";

const configDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(configDir, "..");
const backendPort = process.env.E2E_BACKEND_PORT ?? "18080";
const frontendPort = process.env.E2E_VITE_PORT ?? "4178";
const backendURL = `http://127.0.0.1:${backendPort}`;
const frontendURL = `http://127.0.0.1:${frontendPort}`;

export default defineConfig({
  testDir: "./e2e",
  testMatch: /real-backend-smoke\.spec\.ts/,
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: frontendURL,
    trace: "on-first-retry",
  },
  webServer: [
    {
      command: "bash scripts/e2e-backend.sh",
      cwd: repositoryRoot,
      url: `${backendURL}/readyz`,
      reuseExistingServer: false,
      timeout: 120_000,
      env: {
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
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
});
