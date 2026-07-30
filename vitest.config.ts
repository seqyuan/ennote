import { defineConfig } from "vitest/config";
import path from "path";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    include: ["tests/**/*.test.{ts,tsx}"],
    exclude: ["node_modules", ".next", "ennoworker"],
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname),
    },
  },
});
