import { SignJWT, jwtVerify } from "jose";

export { initializeAdmin } from "../repositories/settings";

const ADMIN_SUBJECT = "admin";
const SESSION_TTL_SECONDS = 7 * 24 * 60 * 60;

const signingKey = (secret: string): Uint8Array =>
  new TextEncoder().encode(secret);

export async function createSession(secret: string): Promise<string> {
  const issuedAt = Math.floor(Date.now() / 1000);

  return new SignJWT({})
    .setProtectedHeader({ alg: "HS256" })
    .setSubject(ADMIN_SUBJECT)
    .setIssuedAt(issuedAt)
    .setExpirationTime(issuedAt + SESSION_TTL_SECONDS)
    .sign(signingKey(secret));
}

export async function verifySession(
  secret: string,
  token: string,
): Promise<boolean> {
  try {
    const { payload } = await jwtVerify(token, signingKey(secret), {
      algorithms: ["HS256"],
      subject: ADMIN_SUBJECT,
    });
    const now = Math.floor(Date.now() / 1000);
    const issuedAt = payload.iat;
    const expiresAt = payload.exp;
    const claims = Object.keys(payload).sort();

    return (
      claims.length === 3 &&
      claims[0] === "exp" &&
      claims[1] === "iat" &&
      claims[2] === "sub" &&
      Number.isSafeInteger(issuedAt) &&
      Number.isSafeInteger(expiresAt) &&
      issuedAt !== undefined &&
      expiresAt !== undefined &&
      issuedAt <= now &&
      expiresAt > now &&
      expiresAt > issuedAt
    );
  } catch {
    return false;
  }
}
