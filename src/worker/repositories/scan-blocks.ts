import type { PlannedPrefixBlock } from "../../shared/ipv4-prefixes";
import type { IpRecord } from "../../shared/ip";

export type ScanBlockStatus = "active" | "completed";

export interface ScanBlock {
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

interface ScanBlockRow {
  asn: number;
  source_prefix: string;
  block_cidr: string;
  status: ScanBlockStatus;
  latest_ip: string | null;
}

const toScanBlock = (row: ScanBlockRow): ScanBlock => ({
  sourcePrefix: row.source_prefix,
  blockCidr: row.block_cidr,
  status: row.status,
  latestIp: row.latest_ip,
});

export class D1ScanBlockRepository {
  constructor(private readonly db: D1Database) {}

  async syncCurrentPlan(asn: number, planned: PlannedPrefixBlock[]): Promise<ScanBlock[]> {
    const insertStatements = planned.map((block) =>
      this.db
        .prepare(
          `INSERT INTO scan_blocks(asn, block_cidr, source_prefix)
           VALUES (?, ?, ?)
           ON CONFLICT(asn, block_cidr) DO UPDATE SET
             source_prefix = excluded.source_prefix,
             updated_at = CURRENT_TIMESTAMP`,
        )
        .bind(asn, block.blockCidr, block.sourcePrefix),
    );
    for (let start = 0; start < insertStatements.length; start += 100) {
      await this.db.batch(insertStatements.slice(start, start + 100));
    }

    const rows = await this.db
      .prepare(
        `SELECT asn, source_prefix, block_cidr, status, latest_ip
         FROM scan_blocks
         WHERE asn = ?`,
      )
      .bind(asn)
      .all<ScanBlockRow>();
    const currentByCidr = new Map(planned.map((block) => [block.blockCidr, block]));
    return rows.results
      .filter((row) => currentByCidr.has(row.block_cidr))
      .sort((left, right) => {
        const leftNetwork = ipv4NetworkNumber(left.block_cidr);
        const rightNetwork = ipv4NetworkNumber(right.block_cidr);
        return leftNetwork - rightNetwork || left.block_cidr.localeCompare(right.block_cidr);
      })
      .map(toScanBlock);
  }

  async find(asn: number, blockCidr: string): Promise<ScanBlock | null> {
    const row = await this.db
      .prepare(
        `SELECT asn, source_prefix, block_cidr, status, latest_ip
         FROM scan_blocks
         WHERE asn = ? AND block_cidr = ?`,
      )
      .bind(asn, blockCidr)
      .first<ScanBlockRow>();
    return row === null ? null : toScanBlock(row);
  }

  async updateProgress(progress: ScanProgress): Promise<boolean> {
    const result = await this.db
      .prepare(
        `UPDATE scan_blocks
         SET status = CASE WHEN status = 'completed' THEN status ELSE ? END,
             latest_ip = COALESCE(?, latest_ip),
             last_reported_at = CURRENT_TIMESTAMP,
             updated_at = CURRENT_TIMESTAMP
         WHERE asn = ? AND block_cidr = ? AND source_prefix = ?`,
      )
      .bind(
        progress.status,
        progress.latestIp,
        progress.asn,
        progress.blockCidr,
        progress.sourcePrefix,
      )
      .run();
    return result.meta.changes === 1;
  }

  async upsertCandidateAndProgress(
    progress: ScanProgress,
    candidate: IpRecord,
  ): Promise<void> {
    const upsertCandidate = this.db
      .prepare(
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
      )
      .bind(candidate.ip, candidate.port, candidate.asn, candidate.region);
    const updateProgress = this.db
      .prepare(
        `UPDATE scan_blocks
         SET status = CASE WHEN status = 'completed' THEN status ELSE ? END,
             latest_ip = COALESCE(?, latest_ip),
             last_reported_at = CURRENT_TIMESTAMP,
             updated_at = CURRENT_TIMESTAMP
         WHERE asn = ? AND block_cidr = ? AND source_prefix = ?`,
      )
      .bind(
        progress.status,
        progress.latestIp ?? candidate.ip,
        progress.asn,
        progress.blockCidr,
        progress.sourcePrefix,
      );
    await this.db.batch([upsertCandidate, updateProgress]);
  }
}

function ipv4NetworkNumber(cidr: string): number {
  const address = cidr.split("/")[0] ?? "0.0.0.0";
  return address.split(".").reduce((value, octet) => value * 256 + Number(octet), 0);
}
