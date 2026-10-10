import { cloudflareTest, readD1Migrations } from "@cloudflare/vitest-plugin";
import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [
    cloudflareTest(async () => ({
      wrangler: { configPath: "./wrangler.jsonc" },
      miniflare: {
        bindings: {
          TEST_MIGRATIONS: await readD1Migrations("migrations"),
        },
      },
    })),
  ],
  test: {
    exclude: [...configDefaults.exclude, "tests/client/**"],
    passWithNoTests: true,
    setupFiles: ["./tests/worker/apply-migrations.ts"],
  },
});
