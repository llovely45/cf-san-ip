import type { ScanBlockStatus } from "./scan-blocks";

export interface ScanCheckpoint {
  sourcePrefix: string;
  blockCidr: string;
  status: ScanBlockStatus;
  latestIp: string | null;
}

export interface ScanProgress {
  asn: number;
  sourcePrefix: string;
  blockCidr: string;
  status: ScanBlockStatus;
  latestIp: string | null;
  agentId: string;
  leaseId: string;
}

export interface ScanBlockLease {
  asn: number;
  sourcePrefix: string;
  blockCidr: string;
  agentId: string;
  leaseId: string;
}

// The scanner force-syncs its latest checkpoint every 10 minutes. Keep a lease
// for three missed sync intervals so a delayed request does not release work.
export const SCAN_BLOCK_LEASE_TIMEOUT_SECONDS = 30 * 60;

interface ScanCheckpointRow {
  source_prefix: string;
  block_cidr: string;
  status: ScanBlockStatus;
  latest_ip: string | null;
}

export class D1ScanCheckpointRepository {
  constructor(private readonly db: D1Database) {}

  async savePlan(asn: number, prefixes: string[]): Promise<void> {
    const prefixesJSON = JSON.stringify(prefixes);
    const previous = await this.db.prepare(
      "SELECT prefixes_json FROM scan_plan_snapshots WHERE asn = ?",
    ).bind(asn).first<{ prefixes_json: string }>();
    const statements: D1PreparedStatement[] = [];
    if (previous !== null && !samePrefixSet(previous.prefixes_json, prefixes)) {
      // A changed route set may add blocks before saved progress. Reset both
      // the legacy cursor and per-block progress so new routes cannot be skipped.
      statements.push(
        this.db.prepare("DELETE FROM scan_checkpoints WHERE asn = ?").bind(asn),
        this.db.prepare("DELETE FROM scan_block_progress WHERE asn = ?").bind(asn),
      );
    }
    statements.push(this.db.prepare(
      `INSERT INTO scan_plan_snapshots(asn, prefixes_json)
       VALUES (?, ?)
       ON CONFLICT(asn) DO UPDATE SET
         prefixes_json = excluded.prefixes_json,
         updated_at = CURRENT_TIMESTAMP`,
    ).bind(asn, prefixesJSON));
    if (statements.length === 1) {
      await statements[0]?.run();
      return;
    }
    await this.db.batch(statements);
  }

  async list(asn: number, currentPrefixes: string[]): Promise<ScanCheckpoint[]> {
    const rows = await this.db.prepare(
      `SELECT source_prefix, block_cidr, status, latest_ip
       FROM scan_checkpoints
       WHERE asn = ?`,
    ).bind(asn).all<ScanCheckpointRow>();
    const current = new Set(currentPrefixes);
    return rows.results
      .filter((row) => current.has(row.source_prefix))
      .sort((left, right) => left.source_prefix.localeCompare(right.source_prefix))
      .map((row) => ({
        sourcePrefix: row.source_prefix,
        blockCidr: row.block_cidr,
        status: row.status,
        latestIp: row.latest_ip,
      }));
  }

  async listBlockProgress(asn: number, currentPrefixes: string[]): Promise<ScanCheckpoint[]> {
    const rows = await this.db.prepare(
      `SELECT source_prefix, block_cidr, status, latest_ip
       FROM scan_block_progress
       WHERE asn = ?`,
    ).bind(asn).all<ScanCheckpointRow>();
    const current = new Set(currentPrefixes);
    return rows.results
      .filter((row) => current.has(row.source_prefix))
      .sort((left, right) =>
        left.source_prefix.localeCompare(right.source_prefix) ||
        left.block_cidr.localeCompare(right.block_cidr),
      )
      .map((row) => ({
        sourcePrefix: row.source_prefix,
        blockCidr: row.block_cidr,
        status: row.status,
        latestIp: row.latest_ip,
      }));
  }

  async isInCurrentPlan(asn: number, sourcePrefix: string): Promise<boolean> {
    const result = await this.db.prepare(
      `SELECT 1 AS present
       FROM scan_plan_snapshots, json_each(scan_plan_snapshots.prefixes_json)
       WHERE scan_plan_snapshots.asn = ? AND json_each.value = ?
       LIMIT 1`,
    ).bind(asn, sourcePrefix).first<{ present: number }>();
    return result !== null;
  }

  async getCurrentPlan(asn: number): Promise<string[] | null> {
    const row = await this.db.prepare(
      "SELECT prefixes_json FROM scan_plan_snapshots WHERE asn = ?",
    ).bind(asn).first<{ prefixes_json: string }>();
    if (row === null) {
      return null;
    }
    try {
      const parsed: unknown = JSON.parse(row.prefixes_json);
      return Array.isArray(parsed) && parsed.every((value) => typeof value === "string")
        ? parsed
        : null;
    } catch {
      return null;
    }
  }

  async isBlockCompleted(asn: number, sourcePrefix: string, blockCidr: string): Promise<boolean> {
    const result = await this.db.prepare(
      `SELECT 1 AS completed
       WHERE EXISTS (
         SELECT 1 FROM scan_block_progress
         WHERE asn = ? AND source_prefix = ? AND block_cidr = ? AND status = 'completed'
       ) OR EXISTS (
         SELECT 1 FROM scan_checkpoints
         WHERE asn = ? AND source_prefix = ? AND block_cidr = ? AND status = 'completed'
       )`,
    ).bind(asn, sourcePrefix, blockCidr, asn, sourcePrefix, blockCidr)
      .first<{ completed: number }>();
    return result !== null;
  }

  async claimBlock(lease: ScanBlockLease): Promise<boolean> {
    const claimed = await this.db.prepare(
      `INSERT INTO scan_block_progress(
         asn, source_prefix, block_cidr, status, lease_agent_id, lease_id, lease_expires_at
       )
       SELECT ?, ?, ?, 'active', ?, ?, datetime('now', '+' || ? || ' seconds')
       WHERE NOT EXISTS (
         SELECT 1 FROM scan_checkpoints
         WHERE asn = ? AND source_prefix = ? AND block_cidr = ? AND status = 'completed'
       )
       ON CONFLICT(asn, source_prefix, block_cidr) DO UPDATE SET
         status = 'active',
         lease_agent_id = excluded.lease_agent_id,
         lease_id = excluded.lease_id,
         lease_expires_at = excluded.lease_expires_at,
         updated_at = CURRENT_TIMESTAMP
       WHERE scan_block_progress.status != 'completed'
         AND (
           scan_block_progress.lease_id IS NULL
           OR scan_block_progress.lease_expires_at IS NULL
           OR scan_block_progress.lease_expires_at <= CURRENT_TIMESTAMP
         )
       RETURNING lease_id`,
    ).bind(
      lease.asn,
      lease.sourcePrefix,
      lease.blockCidr,
      lease.agentId,
      lease.leaseId,
      SCAN_BLOCK_LEASE_TIMEOUT_SECONDS,
      lease.asn,
      lease.sourcePrefix,
      lease.blockCidr,
    ).first<{ lease_id: string }>();
    return claimed?.lease_id === lease.leaseId;
  }

  async releaseBlock(lease: ScanBlockLease): Promise<boolean> {
    const result = await this.db.prepare(
      `UPDATE scan_block_progress
       SET lease_agent_id = NULL,
           lease_id = NULL,
           lease_expires_at = NULL,
           updated_at = CURRENT_TIMESTAMP
       WHERE asn = ? AND source_prefix = ? AND block_cidr = ?
         AND lease_agent_id = ? AND lease_id = ? AND status != 'completed'`,
    ).bind(
      lease.asn,
      lease.sourcePrefix,
      lease.blockCidr,
      lease.agentId,
      lease.leaseId,
    ).run();
    return result.meta.changes === 1;
  }

  async updateProgress(progress: ScanProgress): Promise<boolean> {
    const completed = progress.status === "completed";
    const result = await this.db.prepare(
      `UPDATE scan_block_progress
       SET latest_ip = COALESCE(?, latest_ip),
           status = ?,
           lease_agent_id = CASE WHEN ? = 1 THEN NULL ELSE lease_agent_id END,
           lease_id = CASE WHEN ? = 1 THEN NULL ELSE lease_id END,
           lease_expires_at = CASE
             WHEN ? = 1 THEN NULL
             ELSE datetime('now', '+' || ? || ' seconds')
           END,
           last_reported_at = CURRENT_TIMESTAMP,
           updated_at = CURRENT_TIMESTAMP
       WHERE asn = ? AND source_prefix = ? AND block_cidr = ?
         AND lease_agent_id = ? AND lease_id = ?
         AND lease_expires_at > CURRENT_TIMESTAMP AND status != 'completed'`,
    ).bind(
      progress.latestIp,
      progress.status,
      completed ? 1 : 0,
      completed ? 1 : 0,
      completed ? 1 : 0,
      SCAN_BLOCK_LEASE_TIMEOUT_SECONDS,
      progress.asn,
      progress.sourcePrefix,
      progress.blockCidr,
      progress.agentId,
      progress.leaseId,
    ).run();
    return result.meta.changes === 1;
  }

  async upsertCandidateAndProgress(
    progress: ScanProgress,
    candidate: { ip: string; port: number; asn: number; region: string },
  ): Promise<boolean> {
    const progressStatement = this.db.prepare(
      `UPDATE scan_block_progress
       SET latest_ip = COALESCE(?, latest_ip),
           status = 'active',
           lease_expires_at = datetime('now', '+' || ? || ' seconds'),
           last_reported_at = CURRENT_TIMESTAMP,
           updated_at = CURRENT_TIMESTAMP
       WHERE asn = ? AND source_prefix = ? AND block_cidr = ?
         AND lease_agent_id = ? AND lease_id = ?
         AND lease_expires_at > CURRENT_TIMESTAMP AND status != 'completed'`,
    ).bind(
      progress.latestIp ?? candidate.ip,
      SCAN_BLOCK_LEASE_TIMEOUT_SECONDS,
      progress.asn,
      progress.sourcePrefix,
      progress.blockCidr,
      progress.agentId,
      progress.leaseId,
    );
    const candidateStatement = this.db.prepare(
      `INSERT INTO ip_records(ip, port, asn, region, asn_alias)
       SELECT ?, ?, ?, ?, NULL
       WHERE EXISTS (
         SELECT 1 FROM scan_block_progress
         WHERE asn = ? AND source_prefix = ? AND block_cidr = ?
           AND lease_agent_id = ? AND lease_id = ?
           AND lease_expires_at > CURRENT_TIMESTAMP AND status != 'completed'
       )
       ON CONFLICT(ip) DO UPDATE SET
         port = excluded.port,
         asn_alias = CASE
           WHEN ip_records.asn = excluded.asn THEN ip_records.asn_alias
           ELSE NULL
         END,
         asn = excluded.asn,
         region = excluded.region`,
    ).bind(
      candidate.ip,
      candidate.port,
      candidate.asn,
      candidate.region,
      progress.asn,
      progress.sourcePrefix,
      progress.blockCidr,
      progress.agentId,
      progress.leaseId,
    );
    const statements = [progressStatement, candidateStatement];
    if (progress.status === "completed") {
      statements.push(this.db.prepare(
        `UPDATE scan_block_progress
         SET status = 'completed',
             lease_agent_id = NULL,
             lease_id = NULL,
             lease_expires_at = NULL,
             last_reported_at = CURRENT_TIMESTAMP,
             updated_at = CURRENT_TIMESTAMP
         WHERE asn = ? AND source_prefix = ? AND block_cidr = ?
           AND lease_agent_id = ? AND lease_id = ?
           AND lease_expires_at > CURRENT_TIMESTAMP AND status != 'completed'`,
      ).bind(
        progress.asn,
        progress.sourcePrefix,
        progress.blockCidr,
        progress.agentId,
        progress.leaseId,
      ));
    }
    const results = await this.db.batch(statements);
    return results[0]?.meta.changes === 1;
  }
}

function samePrefixSet(previousJSON: string, current: string[]): boolean {
  try {
    const parsed: unknown = JSON.parse(previousJSON);
    if (!Array.isArray(parsed) || !parsed.every((value): value is string => typeof value === "string")) {
      return false;
    }
    if (parsed.length !== current.length) {
      return false;
    }
    const previous = [...parsed].sort();
    const next = [...current].sort();
    return previous.every((prefix, index) => prefix === next[index]);
  } catch {
    return false;
  }
}
