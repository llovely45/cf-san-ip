import { getCookie } from "hono/cookie";
import { createMiddleware } from "hono/factory";

import type { Env } from "../env";
import { verifySession } from "../services/session-service";

export const ADMIN_SESSION_COOKIE = "admin_session";

export function createAdminSessionMiddleware(fallbackSecret?: string) {
  return createMiddleware<{ Bindings: Env }>(async (context, next) => {
    const secret = context.env?.JWT_SECRET ?? fallbackSecret;
    const token = getCookie(context, ADMIN_SESSION_COOKIE);

    if (
      secret === undefined ||
      token === undefined ||
      !(await verifySession(secret, token))
    ) {
      return context.json({ error: "unauthorized" }, 401);
    }

    await next();
  });
}
