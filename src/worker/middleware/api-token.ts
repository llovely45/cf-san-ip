import { createMiddleware } from "hono/factory";

import type { Env } from "../env";
import { D1SettingsRepository } from "../repositories/settings";

const encoder = new TextEncoder();

function constantTimeEqual(left: string, right: string): boolean {
  const leftBytes = encoder.encode(left);
  const rightBytes = encoder.encode(right);

  let difference = leftBytes.length ^ rightBytes.length;
  for (let index = 0; index < rightBytes.length; index += 1) {
    difference |= (leftBytes[index] ?? 0) ^ rightBytes[index];
  }

  return difference === 0;
}

export function createApiTokenMiddleware(fallbackDb?: D1Database) {
  return createMiddleware<{ Bindings: Env }>(async (context, next) => {
    const db = context.env?.DB ?? fallbackDb;
    if (db === undefined) {
      throw new Error("Worker DB binding is unavailable");
    }

    const { apiToken } = await new D1SettingsRepository(db).get();

    if (apiToken === null || apiToken.length === 0) {
      return context.json({ error: "api_token_not_configured" }, 503);
    }

    const authorization = context.req.header("Authorization");
    if (authorization === undefined || !authorization.startsWith("Bearer ")) {
      return context.json({ error: "unauthorized" }, 401);
    }

    const providedToken = authorization.slice("Bearer ".length);
    if (providedToken.length === 0 || !constantTimeEqual(providedToken, apiToken)) {
      return context.json({ error: "unauthorized" }, 401);
    }

    await next();
  });
}

export const apiTokenMiddleware = createApiTokenMiddleware();
