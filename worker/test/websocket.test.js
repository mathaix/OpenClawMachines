// WebSocket proxy tests
import { describe, it, expect, beforeAll, beforeEach } from "vitest";
import { getWorkerInstance } from "./helpers/worker-instance.js";
import { signJWT, validClaims } from "./helpers/jwt.js";
import { setResolveHandler, resetHandlers } from "./helpers/mock-server.js";

let worker;
let mockPort;

beforeAll(async () => {
  const inst = await getWorkerInstance();
  worker = inst.worker;
  mockPort = inst.mockPort;
});

beforeEach(() => {
  resetHandlers();
});

function wfetch(hostname, path, options = {}) {
  return worker.fetch(`http://${hostname}${path}`, options);
}

describe("WebSocket upgrade detection", () => {
  it("non-WebSocket request forwards to agent normally", async () => {
    setResolveHandler(() => ({
      machine_id: "vm-abc123",
      host_hostname: `127.0.0.1:${mockPort}`,
      proxy_token: "test-proxy-token",
      account_id: "acct-001",
      user_ids: ["user-123"],
    }));

    const token = await signJWT(validClaims({ user_id: "user-123" }));
    const resp = await wfetch(
      "acme.openclawmachines.com",
      "/my-machine/terminal/ws",
      {
        headers: {
          Cookie: `ocm_token=${token}`,
          Origin: "https://openclawmachines.com",
        },
      }
    );
    // Normal HTTP forward — not a 101 since no Upgrade header
    expect(resp.status).not.toBe(101);
  });

  it("WebSocket upgrade triggers proxy path", async () => {
    setResolveHandler(() => ({
      machine_id: "vm-abc123",
      host_hostname: `127.0.0.1:${mockPort}`,
      proxy_token: "test-proxy-token",
      account_id: "acct-001",
      user_ids: ["user-123"],
    }));

    const token = await signJWT(validClaims({ user_id: "user-123" }));
    const resp = await wfetch(
      "acme.openclawmachines.com",
      "/my-machine/terminal/ws",
      {
        headers: {
          Cookie: `ocm_token=${token}`,
          Origin: "https://openclawmachines.com",
          Upgrade: "websocket",
          Connection: "Upgrade",
          "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
          "Sec-WebSocket-Version": "13",
        },
      }
    );
    // Worker enters proxyWebSocket path — either 101 (success) or 502 (upstream fail)
    expect([101, 502]).toContain(resp.status);
  });

  it("WebSocket request without auth returns 401", async () => {
    const resp = await wfetch(
      "acme.openclawmachines.com",
      "/my-machine/terminal/ws",
      {
        headers: {
          Origin: "https://openclawmachines.com",
          Upgrade: "websocket",
          Connection: "Upgrade",
          "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
          "Sec-WebSocket-Version": "13",
        },
      }
    );
    expect(resp.status).toBe(401);
  });
});
