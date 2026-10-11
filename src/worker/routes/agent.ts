import { Hono } from "hono";

import {
  areIPv4ScanBlocksAscending,
  getIPv4ScanPlanSources,
  isIPv4PlannedScanBlock,
  isIPv4ScanBlock,
  isIPv4WithinCIDR,
  normalizeIPv4Prefixes,
} from "../../shared/ipv4-prefixes";
import { validateIpInput } from "../../shared/ip";
import type { Env } from "../env";
import { apiTokenMiddleware, createApiTokenMiddleware } from "../middleware/api-token";
import type { ScanBlockStatus } from "../repositories/scan-blocks";
import {
  D1ScanCheckpointRepository,
  MAX_SCAN_BLOCK_LEASES_PER_AGENT,
  SCAN_BLOCK_LEASE_TIMEOUT_SECONDS,
  type ScanBlockClaim,
  type ScanBlockLease,
  type ScanProgress,
} from "../repositories/scan-checkpoints";
import { fetchOriginatingIPv4Prefixes } from "../services/ripe-prefixes";

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const MAX_CLAIM_CANDIDATES = 64;

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

function parseAgentId(value: unknown): string | null {
  return typeof value === "string" && /^[A-Za-z0-9._:-]{1,128}$/u.test(value) ? value : null;
}

function parseLeaseId(value: unknown): string | null {
  return typeof value === "string" && value.length >= 16 && value.length <= 128 ? value : null;
}

function parseClaimRequestId(value: unknown): string | null {
  return typeof value === "string" && /^[A-Za-z0-9_-]{16,128}$/u.test(value) ? value : null;
}

function parseLeaseInput(input: Record<string, unknown>): Omit<ScanBlockLease, "leaseId"> | null {
  if (
    typeof input.asn !== "number" || !Number.isSafeInteger(input.asn) || input.asn <= 0 ||
    typeof input.sourcePrefix !== "string" || typeof input.blockCidr !== "string" ||
    !isIPv4ScanBlock(input.sourcePrefix, input.blockCidr)
  ) {
    return null;
  }
  const agentId = parseAgentId(input.agentId);
  if (agentId === null) {
    return null;
  }
  return {
    asn: input.asn,
    sourcePrefix: input.sourcePrefix,
    blockCidr: input.blockCidr,
    agentId,
  };
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

    let prefixes: string[];
    try {
      const originating = await fetchOriginatingIPv4Prefixes(asn, fetchImplementation);
      prefixes = normalizeIPv4Prefixes(originating);
      if (prefixes.length === 0) {
        return context.json({ error: "prefix_source_unavailable" }, 502);
      }
    } catch {
      return context.json({ error: "prefix_source_unavailable" }, 502);
    }

    const repository = new D1ScanCheckpointRepository(resolveDb(context));
    await repository.savePlan(asn, prefixes);
    const checkpoints = await repository.list(asn, prefixes);
    const blockCheckpoints = await repository.listBlockProgress(asn, prefixes);
    return context.json({ asn, prefixes, checkpoints, blockCheckpoints });
  });

  routes.post("/claim", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }
    if (!isObject(input)) {
      return context.json({ error: "invalid_lease_request" }, 400);
    }
    const asn = input.asn;
    const agentId = parseAgentId(input.agentId);
    const requestId = parseClaimRequestId(input.requestId);
    const maxBlocks = input.maxBlocks;
    const candidates = input.blocks;
    if (
      typeof asn !== "number" || !Number.isSafeInteger(asn) || asn <= 0 ||
      agentId === null ||
      requestId === null ||
      typeof maxBlocks !== "number" || !Number.isSafeInteger(maxBlocks) ||
      maxBlocks < 1 || maxBlocks > MAX_SCAN_BLOCK_LEASES_PER_AGENT ||
      !Array.isArray(candidates) || candidates.length < 1 || candidates.length > MAX_CLAIM_CANDIDATES
    ) {
      return context.json({ error: "invalid_lease_request" }, 400);
    }

    const repository = new D1ScanCheckpointRepository(resolveDb(context));
    const currentPrefixes = await repository.getCurrentPlan(asn);
    if (currentPrefixes === null) {
      return context.json({ error: "scan_plan_not_initialized" }, 409);
    }
    const currentPrefixSet = new Set(getIPv4ScanPlanSources(currentPrefixes));
    const seen = new Set<string>();
    const seenBlocks = new Set<string>();
    const parsedCandidates: Array<{ sourcePrefix: string; blockCidr: string }> = [];
    for (const candidate of candidates) {
      if (
        !isObject(candidate) ||
        typeof candidate.sourcePrefix !== "string" ||
        typeof candidate.blockCidr !== "string" ||
        !isIPv4PlannedScanBlock(candidate.sourcePrefix, candidate.blockCidr) ||
        !currentPrefixSet.has(candidate.sourcePrefix)
      ) {
        return context.json({ error: "invalid_lease_candidate" }, 400);
      }
      const key = `${candidate.sourcePrefix}\n${candidate.blockCidr}`;
      if (seen.has(key)) {
        return context.json({ error: "duplicate_lease_candidate" }, 400);
      }
      if (seenBlocks.has(candidate.blockCidr)) {
        return context.json({ error: "duplicate_lease_candidate" }, 400);
      }
      seen.add(key);
      seenBlocks.add(candidate.blockCidr);
      parsedCandidates.push({ sourcePrefix: candidate.sourcePrefix, blockCidr: candidate.blockCidr });
    }
    if (!areIPv4ScanBlocksAscending(parsedCandidates.map((candidate) => candidate.blockCidr))) {
      return context.json({ error: "lease_candidates_not_ascending" }, 400);
    }

    const results: Array<{
      asn: number;
      sourcePrefix: string;
      blockCidr: string;
      state: "claimed" | "leased" | "completed";
      leaseId?: string;
    }> = [];
    let claimedCount = 0;
    for (const candidate of parsedCandidates) {
      const base = { asn, ...candidate };
      const lease: ScanBlockClaim = {
        ...base,
        agentId,
        leaseId: crypto.randomUUID(),
        requestId,
      };
      const existingLeaseId = await repository.getLeaseForRequest(lease);
      if (existingLeaseId !== null) {
        results.push({ ...base, state: "claimed", leaseId: existingLeaseId });
        claimedCount++;
      } else if (await repository.claimBlock(lease)) {
        results.push({ ...base, state: "claimed", leaseId: lease.leaseId });
        claimedCount++;
      } else {
        const recoveredLeaseId = await repository.getLeaseForRequest(lease);
        if (recoveredLeaseId !== null) {
          results.push({ ...base, state: "claimed", leaseId: recoveredLeaseId });
          claimedCount++;
        } else {
          const completed = await repository.isBlockCompleted(asn, candidate.sourcePrefix, candidate.blockCidr);
          results.push({ ...base, state: completed ? "completed" : "leased" });
        }
      }
      if (claimedCount >= Math.min(maxBlocks, MAX_SCAN_BLOCK_LEASES_PER_AGENT)) {
        break;
      }
    }
    return context.json({ results, examined: results.length, leaseTimeoutSeconds: SCAN_BLOCK_LEASE_TIMEOUT_SECONDS });
  });

  routes.post("/release", async (context) => {
    let input: unknown;
    try {
      input = await context.req.json();
    } catch {
      return context.json({ error: "invalid_json" }, 400);
    }
    if (!isObject(input)) {
      return context.json({ error: "invalid_lease_request" }, 400);
    }
    const leaseBase = parseLeaseInput(input);
    const leaseId = parseLeaseId(input.leaseId);
    if (leaseBase === null || leaseId === null) {
      return context.json({ error: "invalid_lease_request" }, 400);
    }
    const released = await new D1ScanCheckpointRepository(resolveDb(context)).releaseBlock({
      ...leaseBase,
      leaseId,
    });
    return context.json({ ok: true, released });
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
    const agentId = parseAgentId(input.agentId);
    const leaseId = parseLeaseId(input.leaseId);
    if (
      typeof asn !== "number" ||
      !Number.isSafeInteger(asn) ||
      asn <= 0 ||
      typeof sourcePrefix !== "string" ||
      typeof blockCidr !== "string" ||
      status === null ||
      !isIPv4ScanBlock(sourcePrefix, blockCidr)
    ) {
      return context.json({ error: "invalid_report" }, 400);
    }
    if (agentId === null || leaseId === null) {
      return context.json({ error: "lease_required" }, 409);
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

    const repository = new D1ScanCheckpointRepository(resolveDb(context));
    if (!(await repository.isInCurrentPlan(asn, sourcePrefix))) {
      return context.json({ error: "scan_block_not_found" }, 404);
    }

    const progress: ScanProgress = {
      asn,
      sourcePrefix,
      blockCidr,
      status,
      latestIp,
      agentId,
      leaseId,
    };

    if (input.candidate === undefined) {
      if (!(await repository.updateProgress(progress))) {
        return context.json({ error: "lease_lost" }, 409);
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

    if (!(await repository.upsertCandidateAndProgress(progress, candidate.value))) {
      return context.json({ error: "lease_lost" }, 409);
    }
    return context.json({ ok: true });
  });

  return routes;
}
