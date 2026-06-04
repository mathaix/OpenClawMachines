// HS256 JWT verification and signing using WebCrypto (Cloudflare Workers compatible)

/**
 * Import a raw secret as an HMAC key for HS256 verification.
 * @param {string} secret - The JWT secret string
 * @returns {Promise<CryptoKey>}
 */
async function importKey(secret) {
  const enc = new TextEncoder();
  return crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"]
  );
}

/**
 * Base64url decode to Uint8Array.
 * @param {string} str
 * @returns {Uint8Array}
 */
function base64urlDecode(str) {
  // Add padding
  str = str.replace(/-/g, "+").replace(/_/g, "/");
  while (str.length % 4) str += "=";
  const binary = atob(str);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/**
 * Verify and decode an HS256 JWT.
 * @param {string} token - The JWT string
 * @param {string} secret - The JWT secret
 * @returns {Promise<object|null>} The decoded payload, or null if invalid
 */
export async function verifyJWT(token, secret) {
  const parts = token.split(".");
  if (parts.length !== 3) return null;

  const [headerB64, payloadB64, signatureB64] = parts;

  // Decode header and verify alg
  try {
    const header = JSON.parse(new TextDecoder().decode(base64urlDecode(headerB64)));
    if (header.alg !== "HS256") return null;
  } catch {
    return null;
  }

  // Verify signature
  const key = await importKey(secret);
  const data = new TextEncoder().encode(`${headerB64}.${payloadB64}`);
  const signature = base64urlDecode(signatureB64);

  const valid = await crypto.subtle.verify("HMAC", key, signature, data);
  if (!valid) return null;

  // Decode payload
  try {
    const payload = JSON.parse(new TextDecoder().decode(base64urlDecode(payloadB64)));

    // Check expiry
    if (payload.exp && payload.exp < Math.floor(Date.now() / 1000)) {
      return null;
    }

    return payload;
  } catch {
    return null;
  }
}

/**
 * Extract and verify JWT from ocm_token cookie or Authorization header.
 * @param {Request} request
 * @param {string} secret
 * @returns {Promise<object|null>} The decoded payload, or null if invalid/missing
 */
export async function verifyRequestJWT(request, secret) {
  // 1. Try cookie first (primary for browsers)
  const cookieHeader = request.headers.get("Cookie");
  if (cookieHeader) {
    const cookies = Object.fromEntries(
      cookieHeader.split(";").map((c) => {
        const [key, ...rest] = c.trim().split("=");
        return [key, rest.join("=")];
      })
    );
    const token = cookies["ocm_token"];
    if (token) {
      return verifyJWT(token, secret);
    }
  }

  // 2. Fall back to Authorization header (for non-browser clients)
  const authHeader = request.headers.get("Authorization");
  if (authHeader && authHeader.startsWith("Bearer ")) {
    const token = authHeader.substring(7);
    return verifyJWT(token, secret);
  }

  return null;
}
