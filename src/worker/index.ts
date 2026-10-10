import { Hono } from "hono";

import type { Env } from "./env";

const app = new Hono<{ Bindings: Env }>();

app.notFound((context) => context.json({ error: "Not Found" }, 404));

export { app };
export default app;
