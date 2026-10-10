import type { IpRecord } from "../../shared/ip";

export interface IpRepository {
  upsert(record: IpRecord): Promise<void>;
  updateByIp(
    existingIp: string,
    record: IpRecord,
  ): Promise<"updated" | "not_found" | "conflict">;
  getByIp(ip: string): Promise<IpRecord | null>;
  findAliasByAsn(asn: number): Promise<string | null>;
  setAliasForAsn(asn: number, alias: string | null): Promise<number>;
  deleteByIp(ip: string): Promise<boolean>;
  list(
    page: number,
    pageSize: 20 | 50 | 100,
  ): Promise<{ rows: IpRecord[]; total: number }>;
  sample(limit: number, asn: number | null): Promise<IpRecord[]>;
}

interface IpRecordRow {
  ip: string;
  port: number;
  asn: number;
  region: string;
  asn_alias: string | null;
}

const IP_RECORD_COLUMNS = "ip, port, asn, region, asn_alias";

const toIpRecord = (row: IpRecordRow): IpRecord => ({
  ip: row.ip,
  port: row.port,
  asn: row.asn,
  region: row.region,
  asnAlias: row.asn_alias,
});

const normalizePage = (page: number): number =>
  Number.isSafeInteger(page) && page > 0 ? page : 1;

const normalizeLimit = (limit: number): number =>
  Number.isSafeInteger(limit) && limit > 0 ? limit : 0;

export class D1IpRepository implements IpRepository {
  constructor(private readonly db: D1Database) {}

  async upsert(record: IpRecord): Promise<void> {
    await this.db
      .prepare(
        `INSERT INTO ip_records (ip, port, asn, region, asn_alias)
         VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(ip) DO UPDATE SET
           port = excluded.port,
           asn = excluded.asn,
           region = excluded.region,
           asn_alias = excluded.asn_alias`,
      )
      .bind(record.ip, record.port, record.asn, record.region, record.asnAlias)
      .run();
  }

  async updateByIp(
    existingIp: string,
    record: IpRecord,
  ): Promise<"updated" | "not_found" | "conflict"> {
    const result = await this.db
      .prepare(
        `UPDATE ip_records
         SET ip = ?, port = ?, asn = ?, region = ?, asn_alias = ?
         WHERE ip = ?
           AND NOT EXISTS (
             SELECT 1 FROM ip_records AS existing
             WHERE existing.ip = ? AND existing.ip <> ?
           )`,
      )
      .bind(
        record.ip,
        record.port,
        record.asn,
        record.region,
        record.asnAlias,
        existingIp,
        record.ip,
        existingIp,
      )
      .run();

    if (result.meta.changes === 1) {
      return "updated";
    }

    return (await this.getByIp(existingIp)) === null ? "not_found" : "conflict";
  }

  async getByIp(ip: string): Promise<IpRecord | null> {
    const row = await this.db
      .prepare(
        `SELECT ${IP_RECORD_COLUMNS}
         FROM ip_records
         WHERE ip = ?`,
      )
      .bind(ip)
      .first<IpRecordRow>();

    return row === null ? null : toIpRecord(row);
  }

  async findAliasByAsn(asn: number): Promise<string | null> {
    const row = await this.db
      .prepare(
        `SELECT asn_alias
         FROM ip_records
         WHERE asn = ? AND asn_alias IS NOT NULL
         LIMIT 1`,
      )
      .bind(asn)
      .first<{ asn_alias: string | null }>();

    return row?.asn_alias ?? null;
  }

  async setAliasForAsn(asn: number, alias: string | null): Promise<number> {
    const result = await this.db
      .prepare(
        `UPDATE ip_records
         SET asn_alias = ?
         WHERE asn = ?`,
      )
      .bind(alias, asn)
      .run();

    return result.meta.changes;
  }

  async deleteByIp(ip: string): Promise<boolean> {
    const result = await this.db
      .prepare("DELETE FROM ip_records WHERE ip = ?")
      .bind(ip)
      .run();

    return result.meta.changes > 0;
  }

  async list(
    page: number,
    pageSize: 20 | 50 | 100,
  ): Promise<{ rows: IpRecord[]; total: number }> {
    const normalizedPage = normalizePage(page);
    const offset = (normalizedPage - 1) * pageSize;
    const count = await this.db
      .prepare("SELECT COUNT(*) AS total FROM ip_records")
      .first<{ total: number }>();
    const rows = await this.db
      .prepare(
        `SELECT ${IP_RECORD_COLUMNS}
         FROM ip_records
         ORDER BY ip
         LIMIT ? OFFSET ?`,
      )
      .bind(pageSize, offset)
      .all<IpRecordRow>();

    return {
      rows: rows.results.map(toIpRecord),
      total: count?.total ?? 0,
    };
  }

  async sample(limit: number, asn: number | null): Promise<IpRecord[]> {
    const normalizedLimit = normalizeLimit(limit);
    const statement =
      asn === null
        ? this.db
            .prepare(
              `SELECT ${IP_RECORD_COLUMNS}
               FROM ip_records
               ORDER BY RANDOM()
               LIMIT ?`,
            )
            .bind(normalizedLimit)
        : this.db
            .prepare(
              `SELECT ${IP_RECORD_COLUMNS}
               FROM ip_records
               WHERE asn = ?
               ORDER BY RANDOM()
               LIMIT ?`,
            )
            .bind(asn, normalizedLimit);
    const rows = await statement.all<IpRecordRow>();

    return rows.results.map(toIpRecord);
  }
}
