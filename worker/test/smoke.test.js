// Smoke tests — basic endpoints and routing through the worker
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

describe("/__version endpoint", () => {
  it("returns version from env", async () => {
    const resp = await wfetch("openclawmachines.com", "/__version");
    expect(resp.status).toBe(200);
    const body = await resp.json();
    expect(body.version).toBe("test-1.0.0");
  });

  it("returns JSON content-type", async () => {
    const resp = await wfetch("openclawmachines.com", "/__version");
    expect(resp.headers.get("content-type")).toBe("application/json");
  });
});

describe("unknown host", () => {
  it("returns 404 for unrecognized hostname", async () => {
    const resp = await wfetch("unknown.example.com", "/");
    expect(resp.status).toBe(404);
    const text = await resp.text();
    expect(text).toBe("Unknown host");
  });
});

describe("subdomain without auth", () => {
  it("returns 401 for subdomain request without JWT", async () => {
    const resp = await wfetch("acme.openclawmachines.com", "/some-machine/test");
    expect(resp.status).toBe(401);
  });
});
