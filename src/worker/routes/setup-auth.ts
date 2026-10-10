import { deleteCookie, setCookie } from "hono/cookie";
import { Hono } from "hono";

import type { Env } from "../env";
import { ADMIN_SESSION_COOKIE } from "../middleware/admin-session";
import { D1SettingsRepository, initializeAdmin } from "../repositories/settings";
import { createSession } from "../services/session-service";

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

export function createSetupAuthRoutes(fallbackEnv?: Env) {
  const routes = new Hono<{ Bindings: Env }>();
  const resolveDb = (env: Env): D1Database => {
    const db = env?.DB ?? fallbackEnv?.DB;
    if (db === undefined) {
      throw new Error("Worker DB binding is unavailable");
    }
    return db;
  };
  const resolveSecret = (env: Env): string => {
    const secret = env?.JWT_SECRET ?? fallbackEnv?.JWT_SECRET;
    if (secret === undefined) {
      throw new Error("Worker JWT_SECRET binding is unavailable");
    }
    return secret;
  };

  routes.get("/api/setup/status", async (context) => {
    const password = await new D1SettingsRepository(
      resolveDb(context.env),
    ).getAdminPassword();
    return context.json({ initialized: password !== null });
  });

  routes.post("/api/setup", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }

    if (
      !isObject(input) ||
      typeof input.password !== "string" ||
      input.password.trim().length === 0
    ) {
      return context.json({ error: "password_required" }, 400);
    }

    const initialized = await initializeAdmin(
      resolveDb(context.env),
      input.password,
    );
    if (!initialized) {
      return context.json({ error: "already_initialized" }, 409);
    }

    return context.json({ initialized: true });
  });

  routes.post("/api/auth/login", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }

    if (!isObject(input) || typeof input.password !== "string") {
      return context.json({ error: "password_required" }, 400);
    }

    const settings = new D1SettingsRepository(resolveDb(context.env));
    const password = await settings.getAdminPassword();
    if (password === null) {
      return context.json({ error: "not_initialized" }, 409);
    }
    if (input.password !== password) {
      return context.json({ error: "unauthorized" }, 401);
    }

    const token = await createSession(resolveSecret(context.env));
    setCookie(context, ADMIN_SESSION_COOKIE, token, {
      httpOnly: true,
      secure: true,
      sameSite: "Lax",
      path: "/",
      maxAge: 7 * 24 * 60 * 60,
    });

    return context.json({ authenticated: true });
  });

  routes.post("/api/auth/logout", (context) => {
    deleteCookie(context, ADMIN_SESSION_COOKIE, {
      httpOnly: true,
      secure: true,
      sameSite: "Lax",
      path: "/",
    });
    return context.json({ authenticated: false });
  });

  return routes;
}
