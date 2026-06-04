// CORS behavior tests — preflight and response headers
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

describe("CORS preflight (OPTIONS)", () => {
  it("returns 204 with CORS headers for allowed https origin", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      method: "OPTIONS",
      headers: {
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(204);
    expect(resp.headers.get("Access-Control-Allow-Origin")).toBe(
      "https://openclawmachines.com"
    );
    expect(resp.headers.get("Access-Control-Allow-Credentials")).toBe("true");
    expect(resp.headers.get("Access-Control-Allow-Methods")).toContain("GET");
    expect(resp.headers.get("Access-Control-Allow-Methods")).toContain("POST");
    expect(resp.headers.get("Access-Control-Max-Age")).toBe("86400");
  });

  it("returns 204 for subdomain origin", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      method: "OPTIONS",
      headers: {
        Origin: "https://acme.openclawmachines.com",
      },
    });
    expect(resp.status).toBe(204);
    expect(resp.headers.get("Access-Control-Allow-Origin")).toBe(
      "https://acme.openclawmachines.com"
    );
  });

  it("returns 403 for http (insecure) origin", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      method: "OPTIONS",
      headers: {
        Origin: "http://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(403);
    expect(resp.headers.get("Access-Control-Allow-Origin")).toBeNull();
  });

  it("returns 403 for evil external origin", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      method: "OPTIONS",
      headers: {
        Origin: "https://evil.com",
      },
    });
    expect(resp.status).toBe(403);
  });

  it("returns 403 when no origin is sent", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      method: "OPTIONS",
    });
    expect(resp.status).toBe(403);
  });
});

describe("CORS on error responses", () => {
  it("includes CORS headers on 401 response for allowed origin", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(401);
    expect(resp.headers.get("Access-Control-Allow-Origin")).toBe(
      "https://openclawmachines.com"
    );
    expect(resp.headers.get("Access-Control-Allow-Credentials")).toBe("true");
  });

  it("omits CORS headers for disallowed origin", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test", {
      headers: {
        Origin: "https://evil.com",
      },
    });
    expect(resp.status).toBe(401);
    expect(resp.headers.get("Access-Control-Allow-Origin")).toBeNull();
  });
});
