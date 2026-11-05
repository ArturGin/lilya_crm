import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development the Go API runs on :8080 and Vite proxies /api to it, so the
// browser sees a single origin (cookies + CSRF header work as in production).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": { target: process.env.API_URL ?? "http://localhost:8080", changeOrigin: false } },
  },
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false },
});
