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
}

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

  async updateProgress(progress: ScanProgress): Promise<void> {
    await this.db.prepare(
      `INSERT INTO scan_block_progress(asn, source_prefix, block_cidr, status, latest_ip)
       VALUES (?, ?, ?, ?, ?)
       ON CONFLICT(asn, source_prefix, block_cidr) DO UPDATE SET
         latest_ip = COALESCE(excluded.latest_ip, scan_block_progress.latest_ip),
         status = CASE
           WHEN scan_block_progress.status = 'completed'
             THEN 'completed'
           ELSE excluded.status
         END,
         last_reported_at = CURRENT_TIMESTAMP,
         updated_at = CURRENT_TIMESTAMP`,
    ).bind(
      progress.asn,
      progress.sourcePrefix,
      progress.blockCidr,
      progress.status,
      progress.latestIp,
    ).run();
  }

  async upsertCandidateAndProgress(
    progress: ScanProgress,
    candidate: { ip: string; port: number; asn: number; region: string },
  ): Promise<void> {
    const candidateStatement = this.db.prepare(
      `INSERT INTO ip_records(ip, port, asn, region, asn_alias)
       VALUES (?, ?, ?, ?, NULL)
       ON CONFLICT(ip) DO UPDATE SET
         port = excluded.port,
         asn_alias = CASE
           WHEN ip_records.asn = excluded.asn THEN ip_records.asn_alias
           ELSE NULL
         END,
         asn = excluded.asn,
         region = excluded.region`,
    ).bind(candidate.ip, candidate.port, candidate.asn, candidate.region);
    const progressStatement = this.db.prepare(
      `INSERT INTO scan_block_progress(asn, source_prefix, block_cidr, status, latest_ip)
       VALUES (?, ?, ?, ?, ?)
       ON CONFLICT(asn, source_prefix, block_cidr) DO UPDATE SET
         latest_ip = COALESCE(excluded.latest_ip, scan_block_progress.latest_ip),
         status = CASE
           WHEN scan_block_progress.status = 'completed'
             THEN 'completed'
           ELSE excluded.status
         END,
         last_reported_at = CURRENT_TIMESTAMP,
         updated_at = CURRENT_TIMESTAMP`,
    ).bind(
      progress.asn,
      progress.sourcePrefix,
      progress.blockCidr,
      progress.status,
      progress.latestIp ?? candidate.ip,
    );
    await this.db.batch([candidateStatement, progressStatement]);
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
