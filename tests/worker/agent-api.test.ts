import { env } from "cloudflare:workers";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createAgentRoutes } from "../../src/worker/routes/agent";
import { D1IpRepository } from "../../src/worker/repositories/ip-records";
import { D1SettingsRepository } from "../../src/worker/repositories/settings";
import { D1ScanCheckpointRepository } from "../../src/worker/repositories/scan-checkpoints";

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
    await db.prepare("DELETE FROM scan_checkpoints").run();
    await db.prepare("DELETE FROM scan_plan_snapshots").run();
    await db.prepare("DELETE FROM ip_records").run();
    await new D1SettingsRepository(db).setApiToken("agent-test-token");
  });

  it("returns compact source prefixes and preserves checkpoints across plan refreshes", async () => {
    const routes = makeRoutes(["8.8.9.0/24", "8.8.8.0/23"]);
    const first = await request(routes, `/plan?asn=${asn}`);
    expect(first.status).toBe(200);
    expect(await first.json()).toEqual({
      asn,
      prefixes: ["8.8.8.0/23", "8.8.9.0/24"],
      checkpoints: [],
    });

    const completed = await request(
      routes,
      "/report",
      { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(report({ status: "completed", sourcePrefix: "8.8.8.0/23" })) },
    );
    expect(completed.status).toBe(200);
    const second = await request(makeRoutes(["8.8.8.0/23", "8.8.9.0/24"]), `/plan?asn=${asn}`);
    const secondBody = await second.json() as { prefixes: string[]; checkpoints: Array<{ blockCidr: string; status: string }> };
    expect(secondBody.prefixes).toEqual(["8.8.8.0/23", "8.8.9.0/24"]);
    expect(secondBody.checkpoints).toEqual([
      { sourcePrefix: "8.8.8.0/23", blockCidr: "8.8.8.0/24", status: "completed", latestIp: null },
    ]);
  });

  it("returns the compact source-prefix plan without materializing every /16 row", async () => {
    const response = await request(makeRoutes(["8.8.0.0/14"]), `/plan?asn=${asn}`);
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      asn,
      prefixes: ["8.8.0.0/14"],
      checkpoints: [],
    });
    const oldRows = await db.prepare("SELECT COUNT(*) AS count FROM scan_blocks WHERE asn = ?")
      .bind(asn).first<{ count: number }>();
    expect(oldRows?.count).toBe(0);
  });

  it("accepts new /16 scan progress while the route remains /14", async () => {
    const routes = makeRoutes(["8.8.0.0/14"]);
    const planned = await request(routes, `/plan?asn=${asn}`);
    expect(planned.status).toBe(200);

    const response = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        sourcePrefix: "8.8.0.0/14",
        blockCidr: "8.8.0.0/16",
        latestIp: "8.8.12.34",
      })),
    });

    expect(response.status).toBe(200);
    const checkpoint = await db.prepare(
      "SELECT block_cidr, latest_ip FROM scan_checkpoints WHERE asn = ?",
    ).bind(asn).first();
    expect(checkpoint).toEqual({ block_cidr: "8.8.0.0/16", latest_ip: "8.8.12.34" });
  });

  it("continues accepting legacy /24 progress during agent rollout", async () => {
    const routes = makeRoutes(["8.8.0.0/14"]);
    await request(routes, `/plan?asn=${asn}`);

    const response = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        sourcePrefix: "8.8.0.0/14",
        blockCidr: "8.8.1.0/24",
        latestIp: "8.8.1.34",
      })),
    });

    expect(response.status).toBe(200);
    const checkpoint = await db.prepare(
      "SELECT block_cidr, latest_ip FROM scan_checkpoints WHERE asn = ?",
    ).bind(asn).first();
    expect(checkpoint).toEqual({ block_cidr: "8.8.1.0/24", latest_ip: "8.8.1.34" });
  });

  it("keeps the complete 2744 scan-block plan compact", async () => {
    const prefixes = [
      ...Array.from({ length: 10 }, (_, index) => `${11 + index}.0.0.0/8`),
      ...Array.from({ length: 26 }, (_, index) => `21.${index}.0.0/16`),
      ...Array.from({ length: 153 }, (_, index) =>
        `22.${Math.floor(index / 2)}.${index % 2 === 0 ? 0 : 128}.0/17`,
      ),
      ...Array.from({ length: 5 }, (_, index) => `23.0.${index}.0/24`),
    ];
    const expandedBlockCount = prefixes.reduce((count, cidr) => {
      const prefixLength = Number(cidr.split("/")[1]);
      return count + 2 ** Math.max(16 - prefixLength, 0);
    }, 0);
    expect(expandedBlockCount).toBe(2_744);

    const response = await request(makeRoutes(prefixes), `/plan?asn=${asn}`);
    expect(response.status).toBe(200);
    const body = await response.json() as { prefixes: string[]; checkpoints: unknown[] };
    expect(body.prefixes).toHaveLength(prefixes.length);
    expect(new Set(body.prefixes)).toEqual(new Set(prefixes));
    expect(body.checkpoints).toEqual([]);
    const oldRows = await db.prepare("SELECT COUNT(*) AS count FROM scan_blocks WHERE asn = ?")
      .bind(asn).first<{ count: number }>();
    expect(oldRows?.count).toBe(0);
  });

  it("handles an ASN with tens of thousands of prefixes in a bounded compact response", async () => {
    const prefixes = Array.from({ length: 19_152 }, (_, index) =>
      `11.${Math.floor(index / 256)}.${index % 256}.0/24`,
    );
    const response = await request(makeRoutes(prefixes), `/plan?asn=${asn}`);
    expect(response.status).toBe(200);
    const body = await response.json() as { prefixes: string[]; checkpoints: unknown[] };
    expect(body.prefixes).toHaveLength(19_152);
    expect(body.checkpoints).toEqual([]);
    expect(new TextEncoder().encode(JSON.stringify({ asn, ...body })).byteLength)
      .toBeLessThan(5 * 1024 * 1024);
    const materializedBlocks = await db.prepare("SELECT COUNT(*) AS count FROM scan_blocks WHERE asn = ?")
      .bind(asn).first<{ count: number }>();
    expect(materializedBlocks?.count).toBe(0);
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

  it("resets the cursor when the announced prefix set changes", async () => {
    const routes = makeRoutes(["8.8.8.0/24"]);
    await request(routes, `/plan?asn=${asn}`);
    await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({ status: "completed" })),
    });
    const second = await request(makeRoutes(["8.8.7.0/24", "8.8.8.0/24", "8.8.9.0/24"]), `/plan?asn=${asn}`);
    expect(await second.json()).toEqual({
      asn,
      prefixes: ["8.8.7.0/24", "8.8.8.0/24", "8.8.9.0/24"],
      checkpoints: [],
    });
    const checkpoints = await db.prepare("SELECT source_prefix FROM scan_checkpoints WHERE asn = ?")
      .bind(asn).all<{ source_prefix: string }>();
    expect(checkpoints.results).toEqual([]);
  });

  it("does not regress a completed checkpoint when a stale active report arrives", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({ status: "completed" })),
    });
    const stale = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({ status: "active", latestIp: "8.8.8.7" })),
    });
    expect(stale.status).toBe(200);
    expect(await db.prepare("SELECT status FROM scan_checkpoints WHERE asn = ?")
      .bind(asn).first()).toMatchObject({ status: "completed" });
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
    expect(await db.prepare("SELECT latest_ip FROM scan_checkpoints WHERE asn = ?").bind(asn).first()).toMatchObject({ latest_ip: "8.8.8.7" });
  });
});
