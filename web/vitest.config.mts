import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  test: {
    // Dialog/flow tests drive several async steps with user-event; under a busy
    // parallel run (or in Docker) the 5 s default is too tight.
    testTimeout: 15_000,
    environment: "jsdom",
    globals: false,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}", "mock/**/*.test.{ts,mjs}"],
    css: false,
    restoreMocks: true,
    clearMocks: true,
  },
});
