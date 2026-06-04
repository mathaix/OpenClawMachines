// HS256 JWT signing for tests — mirrors the verification in jwt.js

export const TEST_SECRET = "test-jwt-secret-for-unit-tests-only";

function base64urlEncode(data) {
  return Buffer.from(data).toString("base64url");
}

export async function signJWT(payload, secret = TEST_SECRET) {
  const header = { alg: "HS256", typ: "JWT" };
  const headerB64 = base64urlEncode(JSON.stringify(header));
  const payloadB64 = base64urlEncode(JSON.stringify(payload));
  const data = `${headerB64}.${payloadB64}`;

  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"]
  );
  const signature = await crypto.subtle.sign(
    "HMAC",
    key,
    new TextEncoder().encode(data)
  );
  const sigB64 = Buffer.from(signature).toString("base64url");

  return `${headerB64}.${payloadB64}.${sigB64}`;
}

export function validClaims(overrides = {}) {
  return {
    user_id: "user-123",
    email: "test@example.com",
    exp: Math.floor(Date.now() / 1000) + 3600,
    iat: Math.floor(Date.now() / 1000),
    ...overrides,
  };
}

export function expiredClaims(overrides = {}) {
  return {
    user_id: "user-123",
    email: "test@example.com",
    exp: Math.floor(Date.now() / 1000) - 3600,
    iat: Math.floor(Date.now() / 1000) - 7200,
    ...overrides,
  };
}
