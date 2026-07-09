import path from "node:path";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";

const proxyTarget = process.env.VITE_PROXY_TARGET ?? "http://127.0.0.1:8080";

function demoModeGuard(): Plugin {
  return {
    name: "demo-mode-guard",
    buildStart() {
      const isDemo = process.env.VITE_ENABLE_DEMO_MODE === "true";
      if (isDemo) {
        this.error(
          "VITE_ENABLE_DEMO_MODE=true is not allowed in production builds. " +
            "Demo mode is a development-only feature that bypasses authentication."
        );
      }
    },
  };
}

export default defineConfig(({ mode }) => ({
  plugins: mode === "production" ? [react(), demoModeGuard()] : [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src")
    }
  },
  server: {
    host: "0.0.0.0",
    proxy: {
      "/api": {
        target: proxyTarget,
        // 保留浏览器原始 Host，便于后端进行“同主机 Origin”判定。
        changeOrigin: false,
        ws: true
      }
    }
  },
  build: {
    chunkSizeWarningLimit: 550,
    rollupOptions: {
      output: {
        manualChunks: {
          recharts: ["recharts"],
          "framer-motion": ["framer-motion"],
          xterm: ["@xterm/xterm", "@xterm/addon-fit"],
          "grid-layout": ["react-grid-layout"],
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./vitest.setup.ts",
    globals: true,
    css: true,
    testTimeout: 15_000,
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "coverage",
    },
  }
})
);
