import { env } from "cloudflare:workers";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createAgentRoutes } from "../../src/worker/routes/agent";
import { D1IpRepository } from "../../src/worker/repositories/ip-records";
import { D1SettingsRepository } from "../../src/worker/repositories/settings";

const db = (env as typeof env & { DB: D1Database }).DB;
const asn = 64512;
const fetchResponse = (prefixes: unknown, status = 200) =>
  new Response(JSON.stringify({ data: { prefixes: { v4: { originating: prefixes } } } }), {
    status,
    headers: { "Content-Type": "application/json" },
  });

function makeRoutes(prefixes: unknown = ["8.8.8.0/24"]): ReturnType<typeof createAgentRoutes> {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(fetchResponse(prefixes));
  return createAgentRoutes(db, fetchMock);
}

async function request(
  routes: ReturnType<typeof createAgentRoutes>,
  path: string,
  init: RequestInit = {},
  authorized = true,
): Promise<Response> {
  const headers = new Headers(init.headers);
  if (authorized) {
    headers.set("Authorization", "Bearer agent-test-token");
  }
  return await routes.request(`https://app.test${path}`, { ...init, headers }, { DB: db } as never);
}

const report = (overrides: Record<string, unknown> = {}) => ({
  asn,
  sourcePrefix: "8.8.8.0/24",
  blockCidr: "8.8.8.0/24",
  status: "active",
  ...overrides,
});

describe("agent plan and report API", () => {
  beforeEach(async () => {
    await db.prepare("DELETE FROM scan_blocks").run();
    await db.prepare("DELETE FROM ip_records").run();
    await new D1SettingsRepository(db).setApiToken("agent-test-token");
  });

  it("returns numeric current blocks and preserves completed state across plan refreshes", async () => {
    const routes = makeRoutes(["8.8.9.0/24", "8.8.8.0/23"]);
    const first = await request(routes, `/plan?asn=${asn}`);
    expect(first.status).toBe(200);
    expect(await first.json()).toEqual({
      asn,
      blocks: [
        { sourcePrefix: "8.8.8.0/23", blockCidr: "8.8.8.0/24", status: "active", latestIp: null },
        { sourcePrefix: "8.8.9.0/24", blockCidr: "8.8.9.0/24", status: "active", latestIp: null },
      ],
    });

    await request(
      routes,
      "/report",
      { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(report({ status: "completed", sourcePrefix: "8.8.8.0/23" })) },
    );
    const second = await request(makeRoutes(["8.8.8.0/23"]), `/plan?asn=${asn}`);
    const secondBody = await second.json() as { blocks: Array<{ blockCidr: string; status: string }> };
    expect(secondBody.blocks.find((block) => block.blockCidr === "8.8.8.0/24")).toMatchObject({
      status: "completed",
    });
  });

  it("requests only RIPEstat originating IPv4 prefixes for the selected ASN", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(fetchResponse(["8.8.8.0/24"]));
    const routes = createAgentRoutes(db, fetchMock);
    const response = await request(routes, `/plan?asn=${asn}`);
    expect(response.status).toBe(200);
    const calledURL = new URL(String(fetchMock.mock.calls[0]?.[0]));
    expect(calledURL.searchParams.get("resource")).toBe(String(asn));
    expect(calledURL.searchParams.get("types")).toBe("o");
    expect(calledURL.searchParams.get("af")).toBe("v4");
    expect(calledURL.searchParams.get("noise")).toBe("filter");
    expect(calledURL.searchParams.get("list_prefixes")).toBe("true");
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBeInstanceOf(AbortSignal);
  });

  it("retains withdrawn scan history but omits withdrawn blocks from current plans", async () => {
    const first = await request(makeRoutes(["8.8.8.0/24"]), `/plan?asn=${asn}`);
    expect(first.status).toBe(200);
    const second = await request(makeRoutes(["8.8.9.0/24"]), `/plan?asn=${asn}`);
    expect(await second.json()).toMatchObject({
      blocks: [{ blockCidr: "8.8.9.0/24" }],
    });
    const history = await db.prepare("SELECT block_cidr FROM scan_blocks WHERE asn = ? ORDER BY block_cidr").bind(asn).all<{ block_cidr: string }>();
    expect(history.results.map((row) => row.block_cidr)).toEqual(["8.8.8.0/24", "8.8.9.0/24"]);
  });

  it("fails closed on malformed, non-success, and empty RIPEstat data", async () => {
    const malformed = createAgentRoutes(db, vi.fn<typeof fetch>().mockResolvedValue(
      new Response("not-json", { status: 200 }),
    ));
    const failed = createAgentRoutes(db, vi.fn<typeof fetch>().mockResolvedValue(fetchResponse([], 503)));
    const empty = makeRoutes([]);

    for (const routes of [malformed, failed, empty]) {
      const response = await request(routes, `/plan?asn=${asn}`);
      expect(response.status).toBe(502);
      expect(await response.json()).toMatchObject({ error: "prefix_source_unavailable" });
    }
  });

  it("requires the configured Bearer token and validates ASN", async () => {
    const routes = makeRoutes();
    expect((await request(routes, `/plan?asn=${asn}`, {}, false)).status).toBe(401);
    expect((await request(routes, "/plan?asn=0")).status).toBe(400);
  });

  it("stores candidate plus completed checkpoint and rejects out-of-block candidates", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const valid = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        status: "completed",
        latestIp: "8.8.8.3",
        candidate: { ip: "8.8.8.3", port: 443, asn, region: "US" },
      })),
    });
    expect(valid.status).toBe(200);
    expect(await new D1IpRepository(db).getByIp("8.8.8.3")).toMatchObject({
      port: 443,
      asn,
      region: "US",
    });

    const invalid = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        candidate: { ip: "1.1.1.1", port: 443, asn, region: "US" },
      })),
    });
    expect(invalid.status).toBe(400);
    expect(await new D1IpRepository(db).getByIp("1.1.1.1")).toBeNull();
  });

  it("keeps an existing ASN alias when refreshing a scanner candidate", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    await new D1IpRepository(db).upsert({ ip: "8.8.8.4", port: 443, asn, region: "CA", asnAlias: "DMIT" });
    const response = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        candidate: { ip: "8.8.8.4", port: 443, asn, region: "US" },
      })),
    });
    expect(response.status).toBe(200);
    expect(await new D1IpRepository(db).getByIp("8.8.8.4")).toMatchObject({
      region: "US",
      asnAlias: "DMIT",
    });
  });

  it.each([
    { candidate: { ip: "8.8.8.1", port: 80, asn, region: "US" } },
    { candidate: { ip: "8.8.8.1", port: 443, asn: 64513, region: "US" } },
    { candidate: { ip: "8.8.8.1", port: 443, asn, region: "US West" } },
  ])("rejects invalid candidates: %o", async ({ candidate }) => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const response = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({ candidate })),
    });
    expect(response.status).toBe(400);
  });

  it("only updates progress for a report without a candidate", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const response = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({ latestIp: "8.8.8.7" })),
    });
    expect(response.status).toBe(200);
    expect(await db.prepare("SELECT * FROM ip_records").all()).toMatchObject({ results: [] });
    expect(await db.prepare("SELECT latest_ip FROM scan_blocks WHERE asn = ?").bind(asn).first()).toMatchObject({ latest_ip: "8.8.8.7" });
  });
});
