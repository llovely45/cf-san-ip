import { env } from "cloudflare:workers";
import { beforeEach, describe, expect, it } from "vitest";

import type { IpRecord } from "../../src/shared/ip";
import { createApp } from "../../src/worker/index";
import { D1IpRepository } from "../../src/worker/repositories/ip-records";
import { D1SettingsRepository } from "../../src/worker/repositories/settings";
import { createSession } from "../../src/worker/services/session-service";

const db = (env as typeof env & { DB: D1Database }).DB;
const secret = "test-jwt-secret-for-admin-api";
const worker = createApp({ DB: db, JWT_SECRET: secret });
const cookieName = "admin_session";

const request = async (
  path: string,
  init?: RequestInit,
): Promise<Response> =>
  await worker.fetch(new Request(`https://app.test${path}`, init), {
    DB: db,
    JWT_SECRET: secret,
  });

const jsonRequest = (
  path: string,
  method: string,
  body: unknown,
  cookie: string,
): Promise<Response> =>
  request(path, {
    method,
    headers: {
      "Content-Type": "application/json",
      Cookie: cookie,
    },
    body: JSON.stringify(body),
  });

const makeRecord = (ip: string, asn: number, asnAlias: string | null): IpRecord => ({
  ip,
  port: 443,
  asn,
  region: "US",
  asnAlias,
});

describe("authenticated management APIs", () => {
  let cookie: string;

  beforeEach(async () => {
    await db.prepare("DELETE FROM ip_records").run();
    const settings = new D1SettingsRepository(db);
    await settings.setAdminPassword("admin-example-password");
    await settings.setApiToken(null);
    cookie = `${cookieName}=${await createSession(secret)}`;
  });

  it("protects record and token management from unauthenticated callers", async () => {
    const records = await request("/api/admin/records");
    const token = await request("/api/admin/token");
    expect(records.status).toBe(401);
    expect(token.status).toBe(401);
  });

  it("creates and pages records using the asn_alias wire field", async () => {
    const created = await jsonRequest(
      "/api/admin/records",
      "POST",
      {
        ip: "192.0.2.10",
        port: 8443,
        asn: 906,
        region: "US",
        asn_alias: "DMIT",
      },
      cookie,
    );
    expect(created.status).toBe(201);
    expect(await created.json()).toEqual({
      record: {
        ip: "192.0.2.10",
        port: 8443,
        asn: 906,
        region: "US",
        asn_alias: "DMIT",
      },
    });

    const listed = await request("/api/admin/records?page=1&pageSize=20", {
      headers: { Cookie: cookie },
    });
    expect(listed.status).toBe(200);
    expect(await listed.json()).toEqual({
      rows: [
        {
          ip: "192.0.2.10",
          port: 8443,
          asn: 906,
          region: "US",
          asn_alias: "DMIT",
        },
      ],
      total: 1,
    });

    const invalidPageSize = await request("/api/admin/records?pageSize=10", {
      headers: { Cookie: cookie },
    });
    expect(invalidPageSize.status).toBe(400);
  });

  it("creates a record with an explicit null alias", async () => {
    const repository = new D1IpRepository(db);
    const response = await jsonRequest(
      "/api/admin/records",
      "POST",
      {
        ip: "192.0.2.11",
        port: 443,
        asn: 906,
        region: "US",
        asn_alias: null,
      },
      cookie,
    );

    expect(response.status).toBe(201);
    expect(await response.json()).toEqual({
      record: {
        ip: "192.0.2.11",
        port: 443,
        asn: 906,
        region: "US",
        asn_alias: null,
      },
    });
    expect(await repository.getByIp("192.0.2.11")).toMatchObject({
      asnAlias: null,
    });
  });

  it("supports the remaining allowed page sizes", async () => {
    const repository = new D1IpRepository(db);
    for (let index = 1; index <= 101; index += 1) {
      const secondOctet = Math.floor((index - 1) / 256);
      const lastOctet = ((index - 1) % 256) + 1;
      await repository.upsert(
        makeRecord(`198.51.${secondOctet}.${lastOctet}`, 906, null),
      );
    }

    const pageSize50 = await request(
      "/api/admin/records?page=2&pageSize=50",
      { headers: { Cookie: cookie } },
    );
    const pageSize100 = await request("/api/admin/records?pageSize=100", {
      headers: { Cookie: cookie },
    });

    expect(pageSize50.status).toBe(200);
    const pageSize50Body = (await pageSize50.json()) as {
      rows: unknown[];
      total: number;
    };
    expect(pageSize50Body.total).toBe(101);
    expect(pageSize50Body.rows).toHaveLength(50);
    expect(pageSize100.status).toBe(200);
    const pageSize100Body = (await pageSize100.json()) as { rows: unknown[] };
    expect(pageSize100Body.rows).toHaveLength(100);
  });

  it("renames an IP primary key and updates the record", async () => {
    const repository = new D1IpRepository(db);
    await repository.upsert(makeRecord("192.0.2.20", 906, "OLD"));

    const response = await jsonRequest(
      "/api/admin/records/192.0.2.20",
      "PUT",
      {
        ip: "192.0.2.21",
        port: 9443,
        asn: 906,
        region: "DE",
        asn_alias: "NEW",
      },
      cookie,
    );

    expect(response.status).toBe(200);
    expect(await repository.getByIp("192.0.2.20")).toBeNull();
    expect(await repository.getByIp("192.0.2.21")).toEqual({
      ip: "192.0.2.21",
      port: 9443,
      asn: 906,
      region: "DE",
      asnAlias: "NEW",
    });
  });

  it("clears a record alias when an edit sends an explicit null", async () => {
    const repository = new D1IpRepository(db);
    await repository.upsert(makeRecord("192.0.2.22", 906, "OLD"));

    const response = await jsonRequest(
      "/api/admin/records/192.0.2.22",
      "PUT",
      {
        ip: "192.0.2.22",
        port: 443,
        asn: 906,
        region: "US",
        asn_alias: null,
      },
      cookie,
    );

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      record: {
        ip: "192.0.2.22",
        port: 443,
        asn: 906,
        region: "US",
        asn_alias: null,
      },
    });
    expect(await repository.getByIp("192.0.2.22")).toMatchObject({
      asnAlias: null,
    });
  });

  it("returns conflict without changing either record when a rename target exists", async () => {
    const repository = new D1IpRepository(db);
    await repository.upsert(makeRecord("192.0.2.30", 906, "SOURCE"));
    await repository.upsert(makeRecord("192.0.2.31", 64500, "TARGET"));

    const response = await jsonRequest(
      "/api/admin/records/192.0.2.30",
      "PUT",
      {
        ip: "192.0.2.31",
        port: 443,
        asn: 906,
        region: "US",
        asn_alias: "OVERWRITE",
      },
      cookie,
    );

    expect(response.status).toBe(409);
    expect(await repository.getByIp("192.0.2.30")).toMatchObject({
      asn: 906,
      asnAlias: "SOURCE",
    });
    expect(await repository.getByIp("192.0.2.31")).toMatchObject({
      asn: 64500,
      asnAlias: "TARGET",
    });
  });

  it("returns not found when renaming an unknown old IP key", async () => {
    const response = await jsonRequest(
      "/api/admin/records/192.0.2.39",
      "PUT",
      {
        ip: "192.0.2.40",
        port: 443,
        asn: 906,
        region: "US",
      },
      cookie,
    );
    expect(response.status).toBe(404);
  });

  it("updates and clears aliases across all records, even if existing aliases differ", async () => {
    const repository = new D1IpRepository(db);
    await repository.upsert(makeRecord("192.0.2.41", 906, "FIRST"));
    await repository.upsert(makeRecord("192.0.2.42", 906, "SECOND"));
    await repository.upsert(makeRecord("192.0.2.43", 64500, "OTHER"));

    const setAlias = await jsonRequest(
      "/api/admin/asns/906/alias",
      "PUT",
      { alias: "DMIT" },
      cookie,
    );
    expect(setAlias.status).toBe(200);
    expect(await repository.getByIp("192.0.2.41")).toMatchObject({
      asnAlias: "DMIT",
    });
    expect(await repository.getByIp("192.0.2.42")).toMatchObject({
      asnAlias: "DMIT",
    });
    expect(await repository.getByIp("192.0.2.43")).toMatchObject({
      asnAlias: "OTHER",
    });

    const clearAlias = await request("/api/admin/asns/906/alias", {
      method: "DELETE",
      headers: { Cookie: cookie },
    });
    expect(clearAlias.status).toBe(200);
    expect(await repository.getByIp("192.0.2.41")).toMatchObject({
      asnAlias: null,
    });
    expect(await repository.getByIp("192.0.2.42")).toMatchObject({
      asnAlias: null,
    });
  });

  it("deletes records and manages a plaintext API token including generation", async () => {
    const repository = new D1IpRepository(db);
    await repository.upsert(makeRecord("192.0.2.50", 906, null));

    const deleted = await request("/api/admin/records/192.0.2.50", {
      method: "DELETE",
      headers: { Cookie: cookie },
    });
    expect(deleted.status).toBe(200);
    expect(await repository.getByIp("192.0.2.50")).toBeNull();

    const initialToken = await request("/api/admin/token", {
      headers: { Cookie: cookie },
    });
    expect(await initialToken.json()).toEqual({ token: null });

    const setToken = await jsonRequest(
      "/api/admin/token",
      "PUT",
      { token: "plain-api-token" },
      cookie,
    );
    expect(setToken.status).toBe(200);
    expect(await setToken.json()).toEqual({ token: "plain-api-token" });
    expect((await new D1SettingsRepository(db).get()).apiToken).toBe(
      "plain-api-token",
    );

    const generated = await request("/api/admin/token/generate", {
      method: "POST",
      headers: { Cookie: cookie },
    });
    expect(generated.status).toBe(200);
    const generatedBody = (await generated.json()) as { token: string };
    expect(generatedBody.token).toMatch(/^[a-f0-9]{64}$/u);
    expect((await new D1SettingsRepository(db).get()).apiToken).toBe(
      generatedBody.token,
    );
  });
});
