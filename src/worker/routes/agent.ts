import { Hono } from "hono";

import { isIPv4WithinCIDR, planIPv4Blocks } from "../../shared/ipv4-prefixes";
import { validateIpInput } from "../../shared/ip";
import type { Env } from "../env";
import { apiTokenMiddleware, createApiTokenMiddleware } from "../middleware/api-token";
import { D1ScanBlockRepository } from "../repositories/scan-blocks";
import type { ScanBlockStatus, ScanProgress } from "../repositories/scan-blocks";
import { fetchOriginatingIPv4Prefixes } from "../services/ripe-prefixes";

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

function parseAsn(value: string | null): number | null {
  if (value === null || !/^\d+$/u.test(value)) {
    return null;
  }
  const asn = Number(value);
  return Number.isSafeInteger(asn) && asn > 0 ? asn : null;
}

function parseStatus(value: unknown): ScanBlockStatus | null {
  return value === "active" || value === "completed" ? value : null;
}

export function createAgentRoutes(
  fallbackDb?: D1Database,
  fetchImplementation: typeof fetch = fetch,
) {
  const routes = new Hono<{ Bindings: Env }>();
  routes.use(
    "*",
    fallbackDb === undefined
      ? apiTokenMiddleware
      : createApiTokenMiddleware(fallbackDb),
  );

  const resolveDb = (context: { env: Env }): D1Database => {
    const db = context.env?.DB ?? fallbackDb;
    if (db === undefined) {
      throw new Error("Worker DB binding is unavailable");
    }
    return db;
  };

  routes.get("/plan", async (context) => {
    const asn = parseAsn(new URL(context.req.url).searchParams.get("asn"));
    if (asn === null) {
      return context.json({ error: "invalid_asn" }, 400);
    }

    let planned;
    try {
      const prefixes = await fetchOriginatingIPv4Prefixes(asn, fetchImplementation);
      planned = planIPv4Blocks(asn, prefixes);
      if (planned.length === 0) {
        return context.json({ error: "prefix_source_unavailable" }, 502);
      }
    } catch {
      return context.json({ error: "prefix_source_unavailable" }, 502);
    }

    const blocks = await new D1ScanBlockRepository(resolveDb(context)).syncCurrentPlan(asn, planned);
    return context.json({ asn, blocks });
  });

  routes.post("/report", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }
    if (!isObject(input)) {
      return context.json({ error: "invalid_report" }, 400);
    }

    const asn = input.asn;
    const sourcePrefix = input.sourcePrefix;
    const blockCidr = input.blockCidr;
    const status = parseStatus(input.status);
    if (
      typeof asn !== "number" ||
      !Number.isSafeInteger(asn) ||
      asn <= 0 ||
      typeof sourcePrefix !== "string" ||
      typeof blockCidr !== "string" ||
      status === null
    ) {
      return context.json({ error: "invalid_report" }, 400);
    }

    let latestIp: string | null = null;
    if (input.latestIp !== undefined) {
      if (
        typeof input.latestIp !== "string" ||
        !isIPv4WithinCIDR(input.latestIp, blockCidr)
      ) {
        return context.json({ error: "invalid_latest_ip" }, 400);
      }
      latestIp = input.latestIp;
    }

    const repository = new D1ScanBlockRepository(resolveDb(context));
    const plannedBlock = await repository.find(asn, blockCidr);
    if (plannedBlock === null || plannedBlock.sourcePrefix !== sourcePrefix) {
      return context.json({ error: "scan_block_not_found" }, 404);
    }

    const progress: ScanProgress = {
      asn,
      sourcePrefix,
      blockCidr,
      status,
      latestIp,
    };

    if (input.candidate === undefined) {
      const updated = await repository.updateProgress(progress);
      if (!updated) {
        return context.json({ error: "scan_block_not_found" }, 404);
      }
      return context.json({ ok: true });
    }

    if (!isObject(input.candidate)) {
      return context.json({ error: "invalid_candidate" }, 400);
    }
    const candidateInput = input.candidate;
    const candidate = validateIpInput({
      ip: candidateInput.ip,
      port: candidateInput.port,
      asn: candidateInput.asn,
      region: candidateInput.region,
    });
    if (
      !candidate.ok ||
      candidate.value.asn !== asn ||
      candidate.value.port !== 443 ||
      !isIPv4WithinCIDR(candidate.value.ip, blockCidr)
    ) {
      return context.json({ error: "invalid_candidate" }, 400);
    }

    await repository.upsertCandidateAndProgress(progress, {
      ...candidate.value,
      asnAlias: null,
    });
    return context.json({ ok: true });
  });

  return routes;
}
