// JWT authentication tests
import { describe, it, expect, beforeAll } from "vitest";
import { getWorkerInstance } from "./helpers/worker-instance.js";
import { signJWT, validClaims, expiredClaims, TEST_SECRET } from "./helpers/jwt.js";

let worker;

beforeAll(async () => {
  const inst = await getWorkerInstance();
  worker = inst.worker;
});

function wfetch(hostname, path, options = {}) {
  return worker.fetch(`http://${hostname}${path}`, options);
}

describe("JWT authentication", () => {
  it("accepts valid JWT in ocm_token cookie", async () => {
    const token = await signJWT(validClaims());
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Cookie: `ocm_token=${token}`,
        Origin: "https://openclawmachines.com",
      },
    });
    // Should NOT be 401 — auth passed. Will be 404 (machine not found) or similar.
    expect(resp.status).not.toBe(401);
    expect(resp.status).not.toBe(500);
  });

  it("accepts valid JWT in Authorization Bearer header", async () => {
    const token = await signJWT(validClaims());
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Authorization: `Bearer ${token}`,
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).not.toBe(401);
  });

  it("rejects expired JWT", async () => {
    const token = await signJWT(expiredClaims());
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Cookie: `ocm_token=${token}`,
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(401);
  });

  it("rejects JWT signed with wrong secret", async () => {
    const token = await signJWT(validClaims(), "wrong-secret");
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Cookie: `ocm_token=${token}`,
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(401);
  });

  it("rejects malformed token", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Cookie: "ocm_token=not.a.valid.jwt",
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(401);
  });

  it("rejects request with no auth at all", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(401);
  });

  it("prefers cookie over Bearer header", async () => {
    const validToken = await signJWT(validClaims());
    const expiredToken = await signJWT(expiredClaims());
    // Cookie has valid token, header has expired — should pass (cookie wins)
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Cookie: `ocm_token=${validToken}`,
        Authorization: `Bearer ${expiredToken}`,
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).not.toBe(401);
  });
});
