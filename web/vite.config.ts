import path from "node:path";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

const proxyTarget = process.env.VITE_PROXY_TARGET ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react()],
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
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./vitest.setup.ts",
    globals: true,
    css: true,
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "coverage",
    },
  }
});
