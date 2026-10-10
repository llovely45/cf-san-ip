import { applyD1Migrations } from "cloudflare:test";
import { env } from "cloudflare:workers";

type TestEnv = typeof env & {
  DB: D1Database;
  TEST_MIGRATIONS: Parameters<typeof applyD1Migrations>[1];
};

const testEnv = env as TestEnv;

const migrations = testEnv.TEST_MIGRATIONS;
if (migrations.length === 0) {
  throw new Error("Worker tests require at least one D1 migration");
}

await applyD1Migrations(testEnv.DB, migrations);
