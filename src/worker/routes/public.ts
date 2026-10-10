import { Hono } from "hono";

import { parseGetIpQuery } from "../../shared/ip";
import type { Env } from "../env";
import {
  apiTokenMiddleware,
  createApiTokenMiddleware,
} from "../middleware/api-token";
import {
  getIpOutput,
  ingestBatch,
  InvalidBatchInputError,
} from "../services/ip-service";

export function createPublicRoutes(fallbackDb?: D1Database) {
  const routes = new Hono<{ Bindings: Env }>();
  const resolveDb = (context: { env: Env }): D1Database => {
    const db = context.env?.DB ?? fallbackDb;
    if (db === undefined) {
      throw new Error("Worker DB binding is unavailable");
    }
    return db;
  };
  const tokenMiddleware =
    fallbackDb === undefined
      ? apiTokenMiddleware
      : createApiTokenMiddleware(fallbackDb);

  routes.use("/postip", tokenMiddleware);
  routes.use("/getip", tokenMiddleware);

  routes.post("/postip", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }

    try {
      return context.json(await ingestBatch(resolveDb(context), input));
    } catch (error) {
      if (error instanceof InvalidBatchInputError) {
        return context.json({ error: error.message }, 400);
      }
      throw error;
    }
  });

  routes.get("/getip", async (context) => {
    const parsed = parseGetIpQuery(new URL(context.req.url).searchParams);
    if (!parsed.ok) {
      return context.json({ error: parsed.reason }, 400);
    }

    const output = await getIpOutput(
      resolveDb(context),
      parsed.value.limit,
      parsed.value.asn,
    );
    return context.text(output, 200, {
      "Content-Type": "text/plain; charset=utf-8",
    });
  });

  return routes;
}

export const publicRoutes = createPublicRoutes();
