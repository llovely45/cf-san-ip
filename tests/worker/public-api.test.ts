import { env, exports } from "cloudflare:workers";
import { beforeEach, describe, expect, it } from "vitest";

import type { IpRecord } from "../../src/shared/ip";
import { D1IpRepository } from "../../src/worker/repositories/ip-records";
import { D1SettingsRepository } from "../../src/worker/repositories/settings";

const db = (env as typeof env & { DB: D1Database }).DB;
const worker = (
  exports as unknown as {
    default: { fetch(request: Request): Promise<Response> };
  }
).default;

const makeRecord = (suffix: number, asn: number): IpRecord => ({
  ip: `192.0.2.${suffix}`,
  port: 4000 + suffix,
  asn,
  region: asn === 906 ? "US" : "JP",
  asnAlias: null,
});

const request = (path: string, init?: RequestInit): Promise<Response> =>
  worker.fetch(new Request(`https://app.test${path}`, init));

const authorize = (init: RequestInit = {}): RequestInit => {
  const headers = new Headers(init.headers);
  headers.set("Authorization", "Bearer test-token");
  return { ...init, headers };
};

const postIps = (body: unknown, init: RequestInit = {}): Promise<Response> => {
  const headers = new Headers(init.headers);
  headers.set("Content-Type", "application/json");
  return request(
    "/postip",
    authorize({
      ...init,
      method: "POST",
      headers,
      body: JSON.stringify(body),
    }),
  );
};

describe("public IP APIs", () => {
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

  it("returns a stable unavailable response when the API token is not configured", async () => {
    const response = await request("/getip");

    expect(response.status).toBe(503);
    expect(await response.json()).toEqual({
      error: "api_token_not_configured",
    });
  });

  it("rejects missing, wrong, and non-Bearer authorization", async () => {
    await new D1SettingsRepository(db).setApiToken("test-token");

    const missing = await request("/getip");
    const wrong = await request("/getip", {
      headers: { Authorization: "Bearer wrong-token" },
    });
    const wrongScheme = await request("/getip", {
      headers: { Authorization: "bearer test-token" },
    });

    expect(missing.status).toBe(401);
    expect(wrong.status).toBe(401);
    expect(wrongScheme.status).toBe(401);
    expect(await missing.json()).toEqual({ error: "unauthorized" });
  });

  it("accepts a correctly authorized batch and maps the wire alias field", async () => {
    await new D1SettingsRepository(db).setApiToken("test-token");

    const response = await postIps({
      ips: [
        {
          ip: "1.2.3.4",
          port: 443,
          asn: 906,
          region: "US",
          asn_alias: "FIRST",
        },
      ],
    });

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      accepted: 1,
      rejected: 0,
      errors: [],
    });
    expect(await new D1IpRepository(db).getByIp("1.2.3.4")).toEqual({
      ip: "1.2.3.4",
      port: 443,
      asn: 906,
      region: "US",
      asnAlias: "FIRST",
    });
  });

  it("returns 400 for invalid JSON and a non-array top-level ips value", async () => {
    await new D1SettingsRepository(db).setApiToken("test-token");

    const invalidJson = await request(
      "/postip",
      authorize({
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{",
      }),
    );
    const invalidTopLevel = await postIps({ ips: {} });

    expect(invalidJson.status).toBe(400);
    expect(invalidTopLevel.status).toBe(400);
  });

  it("continues after row failures and lets the last valid duplicate win", async () => {
    await new D1SettingsRepository(db).setApiToken("test-token");

    const response = await postIps({
      ips: [
        {
          ip: "1.2.3.4",
          port: 443,
          asn: 906,
          region: "US",
          asn_alias: "FIRST",
        },
        {
          ip: "not-an-ip",
          port: 443,
          asn: 906,
          region: "US",
        },
        {
          ip: "1.2.3.4",
          port: 8443,
          asn: 906,
          region: "US",
          asn_alias: "SECOND",
        },
        {
          ip: "198.51.100.2",
          port: 80,
          asn: 906,
          region: "DE",
        },
      ],
    });

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      accepted: 3,
      rejected: 1,
      errors: [{ index: 1, ip: "not-an-ip", reason: "ip is invalid" }],
    });

    const repository = new D1IpRepository(db);
    expect(await repository.getByIp("1.2.3.4")).toMatchObject({
      port: 8443,
      asnAlias: "SECOND",
    });
    expect(await repository.getByIp("198.51.100.2")).toMatchObject({
      asnAlias: "SECOND",
    });
  });

  it("returns the default ten rows, filters by ASN, and caps the result at twenty", async () => {
    await new D1SettingsRepository(db).setApiToken("test-token");
    const repository = new D1IpRepository(db);

    for (let index = 1; index <= 25; index += 1) {
      await repository.upsert(makeRecord(index, index % 2 === 0 ? 906 : 64500));
    }

    const defaultResponse = await request("/getip", authorize());
    const defaultBody = await defaultResponse.text();
    const maximumResponse = await request("/getip?limit=999", authorize());
    const maximumBody = await maximumResponse.text();
    const filteredResponse = await request(
      "/getip?asn=906&limit=20",
      authorize(),
    );
    const filteredBody = await filteredResponse.text();

    expect(defaultResponse.status).toBe(200);
    expect(defaultResponse.headers.get("content-type")).toContain(
      "text/plain",
    );
    expect(defaultBody.split("\n")).toHaveLength(10);
    expect(maximumResponse.status).toBe(200);
    expect(maximumBody.split("\n")).toHaveLength(20);
    expect(filteredResponse.status).toBe(200);
    expect(filteredBody.split("\n")).toHaveLength(12);
    expect(filteredBody).not.toContain("#64500");
    expect(filteredBody).toContain("#906");
  });

  it("formats sampled records as numbered plain-text lines", async () => {
    await new D1SettingsRepository(db).setApiToken("test-token");
    const repository = new D1IpRepository(db);
    await repository.upsert({
      ip: "1.2.3.5",
      port: 8443,
      asn: 906,
      region: "US",
      asnAlias: "DMIT",
    });
    await repository.upsert({
      ip: "1.2.3.4",
      port: 443,
      asn: 906,
      region: "US",
      asnAlias: "DMIT",
    });

    const response = await request("/getip?asn=906&limit=2", authorize());

    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe(
      "text/plain; charset=utf-8",
    );
    expect(await response.text()).toBe(
      "1.2.3.4:443#DMIT US 01\n1.2.3.5:8443#DMIT US 02",
    );
  });

  it("returns an empty plain-text response for an empty sample and rejects invalid queries", async () => {
    await new D1SettingsRepository(db).setApiToken("test-token");

    const emptyResponse = await request(
      "/getip?asn=906&limit=20",
      authorize(),
    );
    const invalidResponse = await request(
      "/getip?asn=0&limit=0",
      authorize(),
    );

    expect(emptyResponse.status).toBe(200);
    expect(await emptyResponse.text()).toBe("");
    expect(emptyResponse.headers.get("content-type")).toContain("text/plain");
    expect(invalidResponse.status).toBe(400);
  });
});
