import path from "path"

import react from "@vitejs/plugin-react"
import { defineConfig } from "vitest/config"

const ci = Boolean(process.env.CI)

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    include: ["src/**/*.test.{ts,tsx}"],
    pool: ci ? "threads" : "forks",
    maxWorkers: ci ? 4 : undefined,
  },
})
