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

async function claimLease(
  routes: ReturnType<typeof createAgentRoutes>,
  options: { sourcePrefix?: string; blockCidr?: string; agentId?: string } = {},
): Promise<{ agentId: string; leaseId: string }> {
  const agentId = options.agentId ?? "test-agent";
  const sourcePrefix = options.sourcePrefix ?? "8.8.8.0/24";
  const blockCidr = options.blockCidr ?? "8.8.8.0/24";
  const response = await request(routes, "/claim", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ asn, agentId, maxBlocks: 1, blocks: [{ sourcePrefix, blockCidr }] }),
  });
  if (response.status !== 200) {
    throw new Error(`lease claim failed with HTTP ${response.status}`);
  }
  const body = await response.json() as { results: Array<{ sourcePrefix: string; blockCidr: string; state: string; leaseId?: string }> };
  const claimed = body.results[0];
  if (claimed === undefined || claimed.state !== "claimed" || claimed.sourcePrefix !== sourcePrefix || claimed.blockCidr !== blockCidr || claimed.leaseId === undefined) {
    throw new Error("test block was not claimable");
  }
  return { agentId, leaseId: claimed.leaseId };
}

async function postReport(
  routes: ReturnType<typeof createAgentRoutes>,
  overrides: Record<string, unknown> = {},
  lease?: { agentId: string; leaseId: string },
): Promise<Response> {
  const payload = report(overrides);
  const owner = lease ?? await claimLease(routes, {
    sourcePrefix: String(payload.sourcePrefix),
    blockCidr: String(payload.blockCidr),
  });
  return await request(routes, "/report", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...payload, ...owner }),
  });
}

describe("agent plan and report API", () => {
  beforeEach(async () => {
    await db.prepare("DELETE FROM scan_blocks").run();
    await db.prepare("DELETE FROM scan_block_progress").run();
    await db.prepare("DELETE FROM scan_checkpoints").run();
    await db.prepare("DELETE FROM scan_plan_snapshots").run();
    await db.prepare("DELETE FROM ip_records").run();
    await new D1SettingsRepository(db).setApiToken("agent-test-token");
  });

  it("returns compact source prefixes and preserves per-block progress across plan refreshes", async () => {
    const routes = makeRoutes(["8.8.9.0/24", "8.8.8.0/23"]);
    const first = await request(routes, `/plan?asn=${asn}`);
    expect(first.status).toBe(200);
    expect(await first.json()).toEqual({
      asn,
      prefixes: ["8.8.8.0/23", "8.8.9.0/24"],
      checkpoints: [],
      blockCheckpoints: [],
    });

    const completed = await postReport(routes, { status: "completed", sourcePrefix: "8.8.8.0/23", blockCidr: "8.8.8.0/23" });
    expect(completed.status).toBe(200);
    const second = await request(makeRoutes(["8.8.8.0/23", "8.8.9.0/24"]), `/plan?asn=${asn}`);
    const secondBody = await second.json() as { prefixes: string[]; checkpoints: Array<{ blockCidr: string; status: string }>; blockCheckpoints: Array<{ blockCidr: string; status: string }> };
    expect(secondBody.prefixes).toEqual(["8.8.8.0/23", "8.8.9.0/24"]);
    expect(secondBody.checkpoints).toEqual([]);
    expect(secondBody.blockCheckpoints).toEqual([
      { sourcePrefix: "8.8.8.0/23", blockCidr: "8.8.8.0/23", status: "completed", latestIp: null },
    ]);
  });

  it("returns the compact source-prefix plan without materializing every /16 row", async () => {
    const response = await request(makeRoutes(["8.8.0.0/14"]), `/plan?asn=${asn}`);
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      asn,
      prefixes: ["8.8.0.0/14"],
      checkpoints: [],
      blockCheckpoints: [],
    });
    const oldRows = await db.prepare("SELECT COUNT(*) AS count FROM scan_blocks WHERE asn = ?")
      .bind(asn).first<{ count: number }>();
    expect(oldRows?.count).toBe(0);
  });

  it("accepts new /16 scan progress while the route remains /14", async () => {
    const routes = makeRoutes(["8.8.0.0/14"]);
    const planned = await request(routes, `/plan?asn=${asn}`);
    expect(planned.status).toBe(200);

    const response = await postReport(routes, {
      sourcePrefix: "8.8.0.0/14",
      blockCidr: "8.8.0.0/16",
      latestIp: "8.8.12.34",
    });

    expect(response.status).toBe(200);
    const checkpoint = await db.prepare(
      "SELECT block_cidr, latest_ip FROM scan_block_progress WHERE asn = ?",
    ).bind(asn).first();
    expect(checkpoint).toEqual({ block_cidr: "8.8.0.0/16", latest_ip: "8.8.12.34" });
  });

  it("stores and returns concurrent block progress independently within one source prefix", async () => {
    const routes = makeRoutes(["8.8.0.0/14"]);
    await request(routes, `/plan?asn=${asn}`);

    for (const block of [
      { blockCidr: "8.8.0.0/16", status: "active" },
      { blockCidr: "8.9.0.0/16", status: "completed" },
      { blockCidr: "8.10.0.0/16", status: "active" },
    ]) {
      const response = await postReport(routes, { sourcePrefix: "8.8.0.0/14", ...block });
      expect(response.status).toBe(200);
    }

    const response = await request(makeRoutes(["8.8.0.0/14"]), `/plan?asn=${asn}`);
    const body = await response.json() as { blockCheckpoints: Array<{ sourcePrefix: string; blockCidr: string; status: string }> };
    expect(body.blockCheckpoints.sort((left, right) => left.blockCidr.localeCompare(right.blockCidr))).toEqual([
        { sourcePrefix: "8.8.0.0/14", blockCidr: "8.8.0.0/16", status: "active", latestIp: null },
        { sourcePrefix: "8.8.0.0/14", blockCidr: "8.9.0.0/16", status: "completed", latestIp: null },
        { sourcePrefix: "8.8.0.0/14", blockCidr: "8.10.0.0/16", status: "active", latestIp: null },
    ].sort((left, right) => left.blockCidr.localeCompare(right.blockCidr)));
  });

  it("leases a block to one agent and lets another claim it after release", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const claim = async (agentId: string) => await request(routes, "/claim", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        asn,
        agentId,
        maxBlocks: 1,
        blocks: [{ sourcePrefix: "8.8.8.0/24", blockCidr: "8.8.8.0/24" }],
      }),
    });

    const firstResponse = await claim("machine-a");
    expect(firstResponse.status).toBe(200);
    const first = (await firstResponse.json() as { results: Array<{ state: string; leaseId: string }> }).results[0];
    expect(first?.state).toBe("claimed");
    expect(first?.leaseId).toEqual(expect.any(String));

    const competing = await claim("machine-b");
    expect(competing.status).toBe(200);
    expect(await competing.json()).toMatchObject({ results: [{ state: "leased" }], examined: 1 });

    const released = await request(routes, "/release", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        asn,
        sourcePrefix: "8.8.8.0/24",
        blockCidr: "8.8.8.0/24",
        agentId: "machine-a",
        leaseId: first?.leaseId,
      }),
    });
    expect(released.status).toBe(200);

    const reacquiredResponse = await claim("machine-b");
    const reacquired = (await reacquiredResponse.json() as { results: Array<{ state: string; leaseId: string }> }).results[0];
    expect(reacquiredResponse.status).toBe(200);
    expect(reacquired?.state).toBe("claimed");
    expect(reacquired?.leaseId).toEqual(expect.any(String));
    expect(reacquired?.leaseId).not.toBe(first?.leaseId);
  });

  it("allows only one machine to win simultaneous claims for the same block", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const claim = (agentId: string) => request(routes, "/claim", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        asn,
        agentId,
        maxBlocks: 1,
        blocks: [{ sourcePrefix: "8.8.8.0/24", blockCidr: "8.8.8.0/24" }],
      }),
    });
    const responses = await Promise.all([claim("machine-a"), claim("machine-b")]);
    const bodies = await Promise.all(responses.map(async (response) => {
      expect(response.status).toBe(200);
      return await response.json() as { results: Array<{ state: string }> };
    }));
    const states = bodies.map((body) => body.results[0]?.state).sort();
    expect(states).toEqual(["claimed", "leased"]);
  });

  it("distributes same-ASN blocks to different machines in ascending batches", async () => {
    const routes = makeRoutes(["8.8.0.0/14"]);
    await request(routes, `/plan?asn=${asn}`);
    const claimNext = async (agentId: string, maxBlocks = 2) => {
      const response = await request(routes, "/claim", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          asn,
          agentId,
          maxBlocks,
          blocks: ["8.8.0.0/16", "8.9.0.0/16", "8.10.0.0/16", "8.11.0.0/16"].map((blockCidr) => ({
            sourcePrefix: "8.8.0.0/14",
            blockCidr,
          })),
        }),
      });
      expect(response.status).toBe(200);
      return await response.json() as {
        results: Array<{ sourcePrefix: string; blockCidr: string; state: string; leaseId?: string }>;
        examined: number;
      };
    };

    const first = await claimNext("machine-a");
    const second = await claimNext("machine-b");
    expect(first.results.filter((item) => item.state === "claimed").map((block) => block.blockCidr)).toEqual(["8.8.0.0/16", "8.9.0.0/16"]);
    expect(second.results.filter((item) => item.state === "claimed").map((block) => block.blockCidr)).toEqual(["8.10.0.0/16", "8.11.0.0/16"]);
    expect(first.results.filter((item) => item.state === "claimed").every((block) => (block.leaseId?.length ?? 0) >= 16)).toBe(true);
    expect(first.examined).toBe(2);
    expect(second.examined).toBe(4);

    const waiting = await claimNext("machine-c", 4);
    expect(waiting.results.every((item) => item.state === "leased")).toBe(true);
    expect(waiting.examined).toBe(4);
  });

  it("renews lease on checkpoint reports and rejects stale owners", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const claim = await claimLease(routes, { agentId: "machine-a" });

    const heartbeat = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        agentId: "machine-a",
        leaseId: claim.leaseId,
        latestIp: "8.8.8.9",
      })),
    });
    expect(heartbeat.status).toBe(200);
    const progress = await db.prepare(
      "SELECT latest_ip, lease_agent_id, lease_id, lease_expires_at FROM scan_block_progress WHERE asn = ?",
    ).bind(asn).first<{ latest_ip: string; lease_agent_id: string; lease_id: string; lease_expires_at: string }>();
    expect(progress).toMatchObject({ latest_ip: "8.8.8.9", lease_agent_id: "machine-a", lease_id: claim.leaseId });
    expect(progress?.lease_expires_at).toBeTruthy();

    const stale = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        agentId: "machine-b",
        leaseId: "stale-lease-identifier",
        latestIp: "8.8.8.10",
      })),
    });
    expect(stale.status).toBe(409);
    expect(await db.prepare("SELECT latest_ip FROM scan_block_progress WHERE asn = ?")
      .bind(asn).first()).toEqual({ latest_ip: "8.8.8.9" });
  });

  it("allows a new agent to claim a lease after its checkpoint timeout", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const claim = async (agentId: string) => await request(routes, "/claim", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        asn,
        agentId,
        maxBlocks: 1,
        blocks: [{ sourcePrefix: "8.8.8.0/24", blockCidr: "8.8.8.0/24" }],
      }),
    });
    const firstResponse = await claim("machine-a");
    const first = (await firstResponse.json() as { results: Array<{ state: string; leaseId: string }> }).results[0];
    expect(first?.state).toBe("claimed");
    expect(first?.leaseId).toEqual(expect.any(String));
    await db.prepare(
      "UPDATE scan_block_progress SET lease_expires_at = datetime('now', '-1 second') WHERE asn = ?",
    ).bind(asn).run();

    const reclaimedResponse = await claim("machine-b");
    const reclaimed = (await reclaimedResponse.json() as { results: Array<{ state: string; leaseId: string }> }).results[0];
    expect(reclaimedResponse.status).toBe(200);
    expect(reclaimed?.state).toBe("claimed");
    expect(reclaimed?.leaseId).toEqual(expect.any(String));
    expect(reclaimed?.leaseId).not.toBe(first?.leaseId);

    const staleReport = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report({
        agentId: "machine-a",
        leaseId: first?.leaseId,
        candidate: { ip: "8.8.8.10", port: 443, asn, region: "US" },
      })),
    });
    expect(staleReport.status).toBe(409);
    expect(await new D1IpRepository(db).getByIp("8.8.8.10")).toBeNull();
  });

  it("accepts /24 progress when the announced source prefix is that /24", async () => {
    const routes = makeRoutes(["8.8.1.0/24"]);
    await request(routes, `/plan?asn=${asn}`);

    const response = await postReport(routes, {
      sourcePrefix: "8.8.1.0/24",
      blockCidr: "8.8.1.0/24",
      latestIp: "8.8.1.34",
    });

    expect(response.status).toBe(200);
    const checkpoint = await db.prepare(
      "SELECT block_cidr, latest_ip FROM scan_block_progress WHERE asn = ?",
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
    await postReport(routes, { status: "completed" });
    const second = await request(makeRoutes(["8.8.7.0/24", "8.8.8.0/24", "8.8.9.0/24"]), `/plan?asn=${asn}`);
    expect(await second.json()).toEqual({
      asn,
      prefixes: ["8.8.7.0/24", "8.8.8.0/24", "8.8.9.0/24"],
      checkpoints: [],
      blockCheckpoints: [],
    });
    const checkpoints = await db.prepare("SELECT source_prefix FROM scan_checkpoints WHERE asn = ?")
      .bind(asn).all<{ source_prefix: string }>();
    expect(checkpoints.results).toEqual([]);
    const blockProgress = await db.prepare("SELECT source_prefix FROM scan_block_progress WHERE asn = ?")
      .bind(asn).all<{ source_prefix: string }>();
    expect(blockProgress.results).toEqual([]);
  });

  it("does not regress a completed checkpoint when a stale active report arrives", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const lease = await claimLease(routes);
    await postReport(routes, { status: "completed" }, lease);
    const stale = await postReport(routes, { status: "active", latestIp: "8.8.8.7" }, lease);
    expect(stale.status).toBe(409);
    expect(await db.prepare("SELECT status FROM scan_block_progress WHERE asn = ?")
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

  it("requires an acquired lease for every scan checkpoint report", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const response = await request(routes, "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report()),
    });
    expect(response.status).toBe(409);
    expect(await response.json()).toEqual({ error: "lease_required" });
  });

  it("stores candidate plus completed checkpoint and rejects out-of-block candidates", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const lease = await claimLease(routes);
    const valid = await postReport(routes, {
      status: "completed",
      latestIp: "8.8.8.3",
      candidate: { ip: "8.8.8.3", port: 443, asn, region: "US" },
    }, lease);
    expect(valid.status).toBe(200);
    expect(await new D1IpRepository(db).getByIp("8.8.8.3")).toMatchObject({
      port: 443,
      asn,
      region: "US",
    });

    const invalid = await postReport(routes, {
      candidate: { ip: "1.1.1.1", port: 443, asn, region: "US" },
    }, lease);
    expect(invalid.status).toBe(400);
    expect(await new D1IpRepository(db).getByIp("1.1.1.1")).toBeNull();
  });

  it("keeps an existing ASN alias when refreshing a scanner candidate", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    await new D1IpRepository(db).upsert({ ip: "8.8.8.4", port: 443, asn, region: "CA", asnAlias: "DMIT" });
    const response = await postReport(routes, {
      candidate: { ip: "8.8.8.4", port: 443, asn, region: "US" },
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
    const response = await postReport(routes, { candidate });
    expect(response.status).toBe(400);
  });

  it("only updates progress for a report without a candidate", async () => {
    const routes = makeRoutes();
    await request(routes, `/plan?asn=${asn}`);
    const response = await postReport(routes, { latestIp: "8.8.8.7" });
    expect(response.status).toBe(200);
    expect(await db.prepare("SELECT * FROM ip_records").all()).toMatchObject({ results: [] });
    expect(await db.prepare("SELECT latest_ip FROM scan_block_progress WHERE asn = ?").bind(asn).first()).toMatchObject({ latest_ip: "8.8.8.7" });
  });
});
