import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // The hooks and the skin render into a DOM; the rest does not care.
    environment: "jsdom",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
