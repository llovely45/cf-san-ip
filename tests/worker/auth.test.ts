import { env } from "cloudflare:workers";
import { SignJWT } from "jose";
import { beforeEach, describe, expect, it } from "vitest";

import { createApp } from "../../src/worker/index";
import {
  createSession,
  initializeAdmin,
  verifySession,
} from "../../src/worker/services/session-service";

const db = (env as typeof env & { DB: D1Database }).DB;
const secret = "test-jwt-secret-for-worker-auth";
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

const postJson = (path: string, body: unknown): Promise<Response> =>
  request(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

const cookieFrom = (response: Response): string => {
  const setCookie = response.headers.get("set-cookie");
  if (setCookie === null) {
    throw new Error("session cookie was not set");
  }
  return setCookie.split(";", 1)[0];
};

describe("admin setup and sessions", () => {
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

  it("initializes admin once and stores the explicitly configured plaintext password", async () => {
    expect(await initializeAdmin(db, "admin-example-password")).toBe(true);
    expect(await initializeAdmin(db, "another-password")).toBe(false);

    const response = await request("/api/setup/status");
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ initialized: true });
  });

  it("reports setup status without exposing the password and rejects empty or repeated setup", async () => {
    const initialStatus = await request("/api/setup/status");
    expect(await initialStatus.json()).toEqual({ initialized: false });

    const emptySetup = await postJson("/api/setup", { password: "  " });
    expect(emptySetup.status).toBe(400);

    const setup = await postJson("/api/setup", {
      password: "admin-example-password",
    });
    expect(setup.status).toBe(200);
    expect(await setup.json()).toEqual({ initialized: true });

    const repeatedSetup = await postJson("/api/setup", {
      password: "another-password",
    });
    expect(repeatedSetup.status).toBe(409);
    expect(await repeatedSetup.json()).toEqual({ error: "already_initialized" });
  });

  it("logs in with the plaintext password and sets a secure seven-day session cookie", async () => {
    await initializeAdmin(db, "admin-example-password");

    const rejected = await postJson("/api/auth/login", {
      password: "wrong-password",
    });
    expect(rejected.status).toBe(401);

    const response = await postJson("/api/auth/login", {
      password: "admin-example-password",
    });
    expect(response.status).toBe(200);
    const setCookie = response.headers.get("set-cookie");
    expect(setCookie).toContain(`${cookieName}=`);
    expect(setCookie).toContain("HttpOnly");
    expect(setCookie).toContain("Secure");
    expect(setCookie).toContain("SameSite=Lax");
    expect(setCookie).toContain("Path=/");
    expect(setCookie).toContain("Max-Age=604800");

    const token = cookieFrom(response).slice(cookieName.length + 1);
    expect(await verifySession(secret, token)).toBe(true);
    const payload = JSON.parse(atob(token.split(".")[1]!)) as Record<
      string,
      unknown
    >;
    expect(payload).toEqual(
      expect.objectContaining({ sub: "admin", iat: expect.any(Number), exp: expect.any(Number) }),
    );
    expect(Object.keys(payload).sort()).toEqual(["exp", "iat", "sub"]);
    expect(JSON.stringify(payload)).not.toContain("password");
    expect(JSON.stringify(payload)).not.toContain("token");
  });

  it("rejects invalid and expired JWTs at protected endpoints", async () => {
    const expiredToken = await new SignJWT({})
      .setProtectedHeader({ alg: "HS256" })
      .setSubject("admin")
      .setIssuedAt(Math.floor(Date.now() / 1000) - 60)
      .setExpirationTime(Math.floor(Date.now() / 1000) - 1)
      .sign(new TextEncoder().encode(secret));

    expect(await verifySession(secret, "not-a-jwt")).toBe(false);
    expect(await verifySession(secret, expiredToken)).toBe(false);

    const invalid = await request("/api/admin/token", {
      headers: { Cookie: `${cookieName}=not-a-jwt` },
    });
    const expired = await request("/api/admin/token", {
      headers: { Cookie: `${cookieName}=${expiredToken}` },
    });
    expect(invalid.status).toBe(401);
    expect(expired.status).toBe(401);
  });

  it("allows a signed-in session to use management APIs and clears its cookie on logout", async () => {
    await initializeAdmin(db, "admin-example-password");
    const login = await postJson("/api/auth/login", {
      password: "admin-example-password",
    });
    const cookie = cookieFrom(login);

    const protectedResponse = await request("/api/admin/token", {
      headers: { Cookie: cookie },
    });
    expect(protectedResponse.status).toBe(200);

    const logout = await request("/api/auth/logout", {
      method: "POST",
      headers: { Cookie: cookie },
    });
    expect(logout.status).toBe(200);
    const clearedCookie = logout.headers.get("set-cookie");
    expect(clearedCookie).toContain(`${cookieName}=`);
    expect(clearedCookie).toContain("Max-Age=0");

    const noLongerAuthenticated = await request("/api/admin/token");
    expect(noLongerAuthenticated.status).toBe(401);
  });

  it("creates and verifies a signed administrator session", async () => {
    const token = await createSession(secret);
    expect(await verifySession(secret, token)).toBe(true);
    expect(await verifySession(`${secret}-wrong`, token)).toBe(false);
  });
});
