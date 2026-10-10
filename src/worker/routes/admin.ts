import { Hono } from "hono";

import { validateIpInput } from "../../shared/ip";
import type { IpRecord } from "../../shared/ip";
import type { Env } from "../env";
import { createAdminSessionMiddleware } from "../middleware/admin-session";
import { D1IpRepository } from "../repositories/ip-records";
import { D1SettingsRepository } from "../repositories/settings";

type PageSize = 20 | 50 | 100;
type WireIpRecord = Omit<IpRecord, "asnAlias"> & { asn_alias: string | null };

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const toWireRecord = (record: IpRecord): WireIpRecord => ({
  ip: record.ip,
  port: record.port,
  asn: record.asn,
  region: record.region,
  asn_alias: record.asnAlias,
});

function mapWireRecord(input: unknown): unknown {
  if (!isObject(input)) {
    return input;
  }

  const mapped = { ...input };
  delete mapped.asnAlias;
  if (Object.hasOwn(input, "asn_alias")) {
    if (input.asn_alias === null) {
      delete mapped.asnAlias;
    } else {
      mapped.asnAlias = input.asn_alias;
    }
  }
  delete mapped.asn_alias;
  return mapped;
}

function parsePage(value: string | undefined, defaultValue: number): number | null {
  if (value === undefined) {
    return defaultValue;
  }
  if (!/^\d+$/u.test(value)) {
    return null;
  }
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
}

function parsePageSize(value: string | undefined): PageSize | null {
  if (value === undefined) {
    return 20;
  }
  if (value === "20" || value === "50" || value === "100") {
    return Number(value) as PageSize;
  }
  return null;
}

function parseAsn(value: string): number | null {
  if (!/^\d+$/u.test(value)) {
    return null;
  }
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
}


function shellQuote(value: string): string {
  return `'${value.replace(/'/gu, `'\\''`)}'`;
}

function resolveDb(env: Env, fallbackEnv?: Env): D1Database {
  const db = env?.DB ?? fallbackEnv?.DB;
  if (db === undefined) {
    throw new Error("Worker DB binding is unavailable");
  }
  return db;
}

function resolveSecret(env: Env, fallbackEnv?: Env): string | undefined {
  return env?.JWT_SECRET ?? fallbackEnv?.JWT_SECRET;
}

function randomToken(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join(
    "",
  );
}

export function createAdminRoutes(fallbackEnv?: Env) {
  const routes = new Hono<{ Bindings: Env }>();
  routes.use(
    "*",
    createAdminSessionMiddleware(fallbackEnv?.JWT_SECRET),
  );

  routes.get("/records", async (context) => {
    const params = new URL(context.req.url).searchParams;
    const page = parsePage(params.get("page") ?? undefined, 1);
    const pageSize = parsePageSize(params.get("pageSize") ?? undefined);
    if (page === null || pageSize === null) {
      return context.json({ error: "invalid_pagination" }, 400);
    }

    const result = await new D1IpRepository(
      resolveDb(context.env, fallbackEnv),
    ).list(page, pageSize);
    return context.json({
      rows: result.rows.map(toWireRecord),
      total: result.total,
    });
  });

  routes.post("/records", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }

    const validation = validateIpInput(mapWireRecord(input));
    if (!validation.ok) {
      return context.json({ error: validation.reason }, 400);
    }

    const record: IpRecord = {
      ...validation.value,
      asnAlias: validation.value.asnAlias ?? null,
    };
    await new D1IpRepository(resolveDb(context.env, fallbackEnv)).upsert(record);
    return context.json({ record: toWireRecord(record) }, 201);
  });

  routes.put("/records/:ip", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }

    const validation = validateIpInput(mapWireRecord(input));
    if (!validation.ok) {
      return context.json({ error: validation.reason }, 400);
    }

    const record: IpRecord = {
      ...validation.value,
      asnAlias: validation.value.asnAlias ?? null,
    };
    const result = await new D1IpRepository(
      resolveDb(context.env, fallbackEnv),
    ).updateByIp(context.req.param("ip"), record);

    if (result === "not_found") {
      return context.json({ error: "record_not_found" }, 404);
    }
    if (result === "conflict") {
      return context.json({ error: "ip_conflict" }, 409);
    }
    return context.json({ record: toWireRecord(record) });
  });

  routes.delete("/records/:ip", async (context) => {
    const deleted = await new D1IpRepository(
      resolveDb(context.env, fallbackEnv),
    ).deleteByIp(context.req.param("ip"));
    if (!deleted) {
      return context.json({ error: "record_not_found" }, 404);
    }
    return context.json({ deleted: true });
  });

  routes.put("/asns/:asn/alias", async (context) => {
    const asn = parseAsn(context.req.param("asn"));
    if (asn === null) {
      return context.json({ error: "invalid_asn" }, 400);
    }

    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }

    if (
      !isObject(input) ||
      typeof input.alias !== "string" ||
      input.alias.trim().length === 0 ||
      /\s/u.test(input.alias.trim())
    ) {
      return context.json({ error: "invalid_alias" }, 400);
    }

    const alias = input.alias.trim();
    const updated = await new D1IpRepository(
      resolveDb(context.env, fallbackEnv),
    ).setAliasForAsn(asn, alias);
    return context.json({ asn, alias, updated });
  });

  routes.delete("/asns/:asn/alias", async (context) => {
    const asn = parseAsn(context.req.param("asn"));
    if (asn === null) {
      return context.json({ error: "invalid_asn" }, 400);
    }

    const updated = await new D1IpRepository(
      resolveDb(context.env, fallbackEnv),
    ).setAliasForAsn(asn, null);
    return context.json({ asn, updated });
  });

  routes.get("/token", async (context) => {
    const settings = await new D1SettingsRepository(
      resolveDb(context.env, fallbackEnv),
    ).get();
    return context.json({ token: settings.apiToken });
  });

  routes.put("/token", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }

    if (
      !isObject(input) ||
      typeof input.token !== "string" ||
      input.token.length === 0
    ) {
      return context.json({ error: "invalid_token" }, 400);
    }

    const settings = new D1SettingsRepository(
      resolveDb(context.env, fallbackEnv),
    );
    await settings.setApiToken(input.token);
    return context.json({ token: input.token });
  });

  routes.post("/token/generate", async (context) => {
    const token = randomToken();
    await new D1SettingsRepository(
      resolveDb(context.env, fallbackEnv),
    ).setApiToken(token);
    return context.json({ token });
  });

  routes.get("/agent/install-command", async (context) => {
    const releaseRepository =
      context.env?.CF_SAN_RELEASE_REPOSITORY ?? fallbackEnv?.CF_SAN_RELEASE_REPOSITORY;
    if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/u.test(releaseRepository ?? "")) {
      return context.json({ error: "release_repository_not_configured" }, 503);
    }

    const apiToken = await new D1SettingsRepository(
      resolveDb(context.env, fallbackEnv),
    ).get();
    if (apiToken.apiToken === null || apiToken.apiToken.length === 0) {
      return context.json({ error: "api_token_not_configured" }, 503);
    }

    const origin = new URL(context.req.url).origin;
    if (!origin.startsWith("https://")) {
      return context.json({ error: "https_required" }, 400);
    }
    return context.json({
      command: `curl -fsSL ${shellQuote(`${origin}/install`)} | sudo sh -s -- --url ${shellQuote(origin)} --token ${shellQuote(apiToken.apiToken)}`,
      warning: "此命令包含 API Token，可能留在 shell 历史中，也可能被本机其他用户从进程参数看到。",
    });
  });

  return routes;
}
