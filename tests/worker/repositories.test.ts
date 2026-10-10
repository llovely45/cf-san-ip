import { env } from "cloudflare:workers";
import { beforeEach, describe, expect, it } from "vitest";

import type { IpRecord } from "../../src/shared/ip";
import { D1IpRepository } from "../../src/worker/repositories/ip-records";
import { D1SettingsRepository } from "../../src/worker/repositories/settings";

const db = (env as typeof env & { DB: D1Database }).DB;

const makeRecord = (
  suffix: number,
  asn: number,
  region = "US",
): IpRecord => ({
  ip: `192.0.2.${suffix}`,
  port: 4000 + suffix,
  asn,
  region,
  asnAlias: null,
});

describe("D1 repositories", () => {
  beforeEach(async () => {
    await db.prepare("DELETE FROM ip_records").run();
    await db
      .prepare(
        `UPDATE app_settings
         SET admin_password = ?, api_token = ?
         WHERE singleton_id = ?`,
      )
      .bind(null, null, 1)
      .run();
  });

  it("upserts, reads, and deletes an IP record", async () => {
    const repository = new D1IpRepository(db);
    const record = {
      ip: "1.2.3.4",
      port: 443,
      asn: 906,
      region: "US",
      asnAlias: null,
    } satisfies IpRecord;

    await repository.upsert(record);
    expect(await repository.getByIp(record.ip)).toEqual(record);

    const updated = { ...record, port: 8443, asnAlias: "DMIT" };
    await repository.upsert(updated);
    expect(await repository.getByIp(record.ip)).toEqual(updated);

    expect(await repository.deleteByIp(record.ip)).toBe(true);
    expect(await repository.getByIp(record.ip)).toBeNull();
    expect(await repository.deleteByIp(record.ip)).toBe(false);
  });

  it("returns a total and pages records using the requested page size", async () => {
    const repository = new D1IpRepository(db);
    const records = Array.from({ length: 21 }, (_, index) =>
      makeRecord(index + 1, 906, index % 2 === 0 ? "US" : "DE"),
    );

    for (const record of records) {
      await repository.upsert(record);
    }

    const firstPage = await repository.list(1, 20);
    const secondPage = await repository.list(2, 20);

    expect(firstPage.total).toBe(records.length);
    expect(firstPage.rows).toHaveLength(20);
    expect(secondPage.total).toBe(records.length);
    expect(secondPage.rows).toHaveLength(1);
    expect(
      new Set([...firstPage.rows, ...secondPage.rows].map((row) => row.ip)),
    ).toEqual(new Set(records.map((record) => record.ip)));
  });

  it("propagates and clears an ASN alias across all matching records", async () => {
    const repository = new D1IpRepository(db);
    const matchingRecords = [makeRecord(10, 906), makeRecord(11, 906, "DE")];
    const otherRecord = makeRecord(12, 64500, "JP");

    for (const record of [...matchingRecords, otherRecord]) {
      await repository.upsert(record);
    }

    expect(await repository.findAliasByAsn(906)).toBeNull();
    expect(await repository.setAliasForAsn(906, "DMIT")).toBe(2);
    expect(await repository.findAliasByAsn(906)).toBe("DMIT");
    for (const record of matchingRecords) {
      expect(await repository.getByIp(record.ip)).toMatchObject({
        asnAlias: "DMIT",
      });
    }
    expect(await repository.getByIp(otherRecord.ip)).toMatchObject({
      asnAlias: null,
    });

    expect(await repository.setAliasForAsn(906, null)).toBe(2);
    expect(await repository.findAliasByAsn(906)).toBeNull();
    expect(await repository.setAliasForAsn(64501, "unused")).toBe(0);
  });

  it("samples from all records or only the requested ASN", async () => {
    const repository = new D1IpRepository(db);
    const records = [
      makeRecord(20, 906),
      makeRecord(21, 906),
      makeRecord(22, 906),
      makeRecord(23, 64500, "JP"),
    ];

    for (const record of records) {
      await repository.upsert(record);
    }

    const asnRows = await repository.sample(10, 906);
    expect(asnRows).toHaveLength(3);
    expect(asnRows.every((row) => row.asn === 906)).toBe(true);

    const allRows = await repository.sample(2, null);
    expect(allRows).toHaveLength(2);
    expect(
      allRows.every((row) => records.some((record) => record.ip === row.ip)),
    ).toBe(true);
  });

  it("reads the singleton settings row and persists its credentials", async () => {
    const repository = new D1SettingsRepository(db);

    expect(await repository.get()).toEqual({
      adminPassword: null,
      apiToken: null,
    });
    expect(await repository.initializeAdmin("admin-password")).toBe(true);
    expect(await repository.initializeAdmin("another-password")).toBe(false);

    await repository.setApiToken("api-token");
    expect(await repository.get()).toEqual({
      adminPassword: "admin-password",
      apiToken: "api-token",
    });

    await repository.setAdminPassword(null);
    await repository.setApiToken(null);
    expect(await repository.get()).toEqual({
      adminPassword: null,
      apiToken: null,
    });
  });
});
