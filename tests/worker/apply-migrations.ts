import { applyD1Migrations } from "cloudflare:test";
import { env } from "cloudflare:workers";

type TestEnv = typeof env & {
  DB: D1Database;
  TEST_MIGRATIONS: Parameters<typeof applyD1Migrations>[1];
};

const testEnv = env as TestEnv;

await applyD1Migrations(testEnv.DB, testEnv.TEST_MIGRATIONS);
