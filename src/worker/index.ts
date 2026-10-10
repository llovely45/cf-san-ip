import { Hono } from "hono";

import type { Env } from "./env";
import { createPublicRoutes } from "./routes/public";

export function createApp(env: Env): Hono<{ Bindings: Env }> {
  const app = new Hono<{ Bindings: Env }>();

  app.route("/", createPublicRoutes(env.DB));
  app.notFound((context) => context.json({ error: "Not Found" }, 404));

  return app;
}

const app = new Hono<{ Bindings: Env }>();

app.route("/", createPublicRoutes());
app.notFound((context) => context.json({ error: "Not Found" }, 404));

export { app };
export default app;
