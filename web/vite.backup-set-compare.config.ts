import path from "node:path";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const directory = process.env.XIRANG_COMPARE_BROWSER_FIXTURE_DIR;
const proxy = process.env.VITE_PROXY_TARGET;
if (!directory || !proxy) throw new Error("comparison browser fixture and proxy are required");

export default defineConfig({
  plugins: [react()],
  cacheDir: path.join(directory, "vite-cache"),
  resolve: { alias: { "@": path.resolve(path.dirname(fileURLToPath(import.meta.url)), "src") } },
  server: { host: "127.0.0.1", watch: null, proxy: { "/api": { target: proxy, changeOrigin: false, ws: true } } },
});
