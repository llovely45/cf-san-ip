import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "jsdom",
    include: [
      "tests/shared/**/*.{test,spec}.?(c|m)[jt]s?(x)",
      "tests/client/**/*.{test,spec}.?(c|m)[jt]s?(x)",
    ],
  },
});
