import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 开发时前端跑在 5173，管理 API 跑在 8092；生产由 admin-api 直接托管 dist。
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/admin": { target: "http://127.0.0.1:8092", changeOrigin: true },
      "/healthz": { target: "http://127.0.0.1:8092", changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: false,
  },
});
