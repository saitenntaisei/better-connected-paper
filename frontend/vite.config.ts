/// <reference types="node" />
import { loadEnv } from "vite";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  // BACKEND_URL wins when set (e.g. docker-compose pointing at the backend
  // service); otherwise fall back to VITE_API_BASE for .env-driven setups,
  // then to localhost for plain host-native dev.
  const backendTarget =
    process.env.BACKEND_URL || env.VITE_API_BASE || "http://localhost:8080";
  return {
    plugins: [react()],
    server: {
      host: "0.0.0.0",
      port: 5173,
      proxy: {
        "/api": {
          target: backendTarget,
          changeOrigin: true,
        },
      },
    },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: "./src/test/setup.ts",
      exclude: ["**/node_modules/**", "**/dist/**", "e2e/**"],
    },
  };
});
