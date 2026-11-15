import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 开发时前端跑在 5173，管理 API 默认在 8092；地址随环境变化，可用
// SMARTPIPE_ADMIN_DEV_URL 覆盖。生产由 admin-api 直接托管 dist，前端只发相对路径。
const adminDevURL = process.env.SMARTPIPE_ADMIN_DEV_URL || "http://127.0.0.1:8092";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/admin": { target: adminDevURL, changeOrigin: true },
      "/healthz": { target: adminDevURL, changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: false,
  },
});
