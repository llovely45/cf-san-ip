import { Hono } from "hono";

import type { Env } from "./env";
import { createAdminRoutes } from "./routes/admin";
import { createAgentRoutes } from "./routes/agent";
import { createPublicRoutes } from "./routes/public";
import { createSetupAuthRoutes } from "./routes/setup-auth";
import { createInstallerRoutes } from "./routes/installer";

export function createApp(env?: Env): Hono<{ Bindings: Env }> {
  const app = new Hono<{ Bindings: Env }>();

  app.route("/", createPublicRoutes(env?.DB));
  app.route("/", createSetupAuthRoutes(env));
  app.route("/api/admin", createAdminRoutes(env));
  app.route("/api/agent/v1", createAgentRoutes(env?.DB));
  app.route("/", createInstallerRoutes(env?.CF_SAN_RELEASE_REPOSITORY));
  app.notFound((context) => context.json({ error: "Not Found" }, 404));

  return app;
}

const app = createApp();

export { app };
export default app;
