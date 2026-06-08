// WebSocket proxy tests
import { describe, it, expect, beforeAll, beforeEach } from "vitest";
import { getWorkerInstance } from "./helpers/worker-instance.js";
import { signJWT, validClaims } from "./helpers/jwt.js";
import { getLastAgentRequest, setResolveHandler, resetHandlers } from "./helpers/mock-server.js";

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
    expect(resp.status).toBe(200);
    const body = await resp.json();
    expect(body.url).toBe("/proxy/vm-abc123/terminal/ws");
    expect(body.headers["x-proxy-token"]).toBe("test-proxy-token");
    expect(getLastAgentRequest()).toMatchObject({
      method: "GET",
      url: "/proxy/vm-abc123/terminal/ws",
    });
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
    expect(resp.status).toBe(101);
    expect(resp.webSocket).toBeTruthy();
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
