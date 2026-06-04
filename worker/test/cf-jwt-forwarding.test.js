// CF JWT forwarding tests — verifies Worker correctly receives/parses CF Access JWTs
import { describe, it, expect, beforeAll } from "vitest";
import { getWorkerInstance } from "./helpers/worker-instance.js";

let worker;

beforeAll(async () => {
  const inst = await getWorkerInstance();
  worker = inst.worker;
});

function wfetch(hostname, path, options = {}) {
  return worker.fetch(`http://${hostname}${path}`, options);
}

describe("CF JWT forwarding via /__auth-debug", () => {
  it("returns cf_jwt_header_present: true when Cf-Access-Jwt-Assertion is set", async () => {
    const resp = await wfetch("openclawmachines.com", "/__auth-debug", {
      headers: {
        "Cf-Access-Jwt-Assertion": "fake-jwt-token-for-testing",
      },
    });
    expect(resp.status).toBe(200);
    const body = await resp.json();
    expect(body.cf_jwt_header_present).toBe(true);
    expect(body.cf_jwt_header_len).toBeGreaterThan(0);
  });

  it("returns cf_jwt_header_present: false when header is absent", async () => {
    const resp = await wfetch("openclawmachines.com", "/__auth-debug");
    expect(resp.status).toBe(200);
    const body = await resp.json();
    expect(body.cf_jwt_header_present).toBe(false);
    expect(body.cf_jwt_header_len).toBe(0);
  });

  it("returns CF headers in cfHeaders object", async () => {
    const resp = await wfetch("openclawmachines.com", "/__auth-debug", {
      headers: {
        "Cf-Access-Jwt-Assertion": "test-jwt-value",
        "Cf-Access-Authenticated-User-Email": "user@example.com",
      },
    });
    expect(resp.status).toBe(200);
    const body = await resp.json();
    expect(body.cf_headers).toBeDefined();
    expect(body.cf_headers["cf-access-authenticated-user-email"]).toBe("user@example.com");
    // JWT value should be masked (length only)
    expect(body.cf_headers["cf-access-jwt-assertion"]).toMatch(/^\(len=\d+\)$/);
  });
});
