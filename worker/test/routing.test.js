// KV route lookup and backend resolve fallback tests
import { describe, it, expect, beforeAll, beforeEach } from "vitest";
import vm from "node:vm";
import { getWorkerInstance } from "./helpers/worker-instance.js";
import { signJWT, validClaims } from "./helpers/jwt.js";
import { setResolveHandler, resetHandlers } from "./helpers/mock-server.js";
import {
  frameAncestorsDirective,
  hermesDashboardBasePath,
  injectHermesDashboardPathShim,
  prefixHermesDashboardAssetPaths,
  shouldInjectHermesDashboardPathShim,
} from "../worker.js";

let worker;

beforeAll(async () => {
  const inst = await getWorkerInstance();
  worker = inst.worker;
});

beforeEach(() => {
  resetHandlers();
});

function wfetch(hostname, path, options = {}) {
  return worker.fetch(`http://${hostname}${path}`, options);
}

async function authHeaders(overrides = {}) {
  const token = await signJWT(validClaims(overrides));
  return { Cookie: `ocm_token=${token}` };
}

describe("KV miss → backend resolve fallback", () => {
  it("returns 404 when resolve returns no route", async () => {
    setResolveHandler(() => null);
    const auth = await authHeaders();
    const resp = await wfetch(
      "unknown-acct.openclawmachines.com",
      "/nonexistent/test",
      {
        headers: {
          ...auth,
          Origin: "https://openclawmachines.com",
        },
      }
    );
    expect(resp.status).toBe(404);
    const text = await resp.text();
    expect(text).toContain("Machine not found");
  });

  it("returns 403 when resolved route has wrong user_ids", async () => {
    setResolveHandler(() => ({
      machine_id: "vm-xyz",
      host_hostname: "localhost:9999",
      proxy_token: "tok",
      account_id: "acct-other",
      user_ids: ["other-user"],
    }));
    const auth = await authHeaders({ user_id: "user-123" });
    const resp = await wfetch(
      "other-team.openclawmachines.com",
      "/some-machine/test",
      {
        headers: {
          ...auth,
          Origin: "https://openclawmachines.com",
        },
      }
    );
    expect(resp.status).toBe(403);
  });

  it("returns 502 when route is missing host_hostname", async () => {
    setResolveHandler(() => ({
      machine_id: "vm-xyz",
      host_hostname: "",
      proxy_token: "tok",
      account_id: "acct-001",
      user_ids: ["user-123"],
    }));
    const auth = await authHeaders({ user_id: "user-123" });
    const resp = await wfetch(
      "test-acct.openclawmachines.com",
      "/my-machine/test",
      {
        headers: {
          ...auth,
          Origin: "https://openclawmachines.com",
        },
      }
    );
    expect(resp.status).toBe(502);
  });

  it("returns 502 when route is missing machine_id", async () => {
    setResolveHandler(() => ({
      machine_id: "",
      host_hostname: "some-host.example.com",
      proxy_token: "tok",
      account_id: "acct-001",
      user_ids: ["user-123"],
    }));
    const auth = await authHeaders({ user_id: "user-123" });
    const resp = await wfetch(
      "test-acct.openclawmachines.com",
      "/my-machine/test",
      {
        headers: {
          ...auth,
          Origin: "https://openclawmachines.com",
        },
      }
    );
    expect(resp.status).toBe(502);
  });
});

describe("subdomain without machine path", () => {
  it("returns 404 when no machine slug in path", async () => {
    const auth = await authHeaders();
    const resp = await wfetch("acme.openclawmachines.com", "/", {
      headers: {
        ...auth,
        Origin: "https://openclawmachines.com",
      },
    });
    expect(resp.status).toBe(404);
    const text = await resp.text();
    expect(text).toContain("specify a machine path");
  });
});

describe("Hermes dashboard routing", () => {
  it("derives the Hermes dashboard prefix from the routed machine path", () => {
    expect(hermesDashboardBasePath("my-machine", "/dashboard")).toBe("/my-machine/dashboard");
    expect(hermesDashboardBasePath("my-machine", "/dashboard/chat")).toBe("/my-machine/dashboard");
    expect(hermesDashboardBasePath("my-machine", "/terminal/ws")).toBe("");
  });

  it("injects a base-path shim into dashboard HTML", async () => {
    const html = injectHermesDashboardPathShim(
      "<!doctype html><html><head><title>Hermes</title></head><body></body></html>"
    );

    expect(html).toContain("ocm-hermes-dashboard-path-shim");
    expect(html).toContain("__OCM_HERMES_DASHBOARD_PATH_SHIM__");
    expect(html).toContain("</script></head>");
  });

  it("prefixes root-relative Hermes dashboard asset paths", () => {
    const html = prefixHermesDashboardAssetPaths(
      '<html><head><link rel="stylesheet" href="/assets/index.css"><link rel="icon" href="/favicon.ico"><script type="module" src="/assets/index.js"></script></head></html>',
      "/my-machine/dashboard"
    );

    expect(html).toContain('href="/my-machine/dashboard/assets/index.css"');
    expect(html).toContain('src="/my-machine/dashboard/assets/index.js"');
    expect(html).toContain('href="/my-machine/dashboard/favicon.ico"');
  });

  it("injects the routed dashboard prefix for older Hermes builds", () => {
    const html = injectHermesDashboardPathShim(
      "<html><head></head><body></body></html>",
      "/my-machine/dashboard"
    );

    expect(html).toContain('var OCM_BASE_PATH = "/my-machine/dashboard";');
    expect(html).toContain("window.__HERMES_BASE_PATH__ = OCM_BASE_PATH");
  });

  it("injects the operator base domain into the dashboard parent-origin allowlist", () => {
    const html = injectHermesDashboardPathShim(
      "<html><head></head><body></body></html>",
      "/my-machine/dashboard",
      "example.com"
    );

    expect(html).toContain('var OCM_BASE_DOMAIN = "example.com";');
    expect(html).toContain('host === OCM_BASE_DOMAIN');
    expect(html).toContain('host.endsWith("." + OCM_BASE_DOMAIN)');
  });

  it("builds frame-ancestor CSP for custom operator domains", () => {
    expect(frameAncestorsDirective("example.com")).toBe("frame-ancestors 'self' example.com *.example.com");
    expect(frameAncestorsDirective("localhost")).toBe("frame-ancestors 'self'");
  });

  it("only enables dashboard shim injection for dashboard HTML", () => {
    const htmlHeaders = new Headers({ "Content-Type": "text/html; charset=utf-8" });
    const jsonHeaders = new Headers({ "Content-Type": "application/json" });

    expect(shouldInjectHermesDashboardPathShim("/dashboard/", htmlHeaders)).toBe(true);
    expect(shouldInjectHermesDashboardPathShim("/dashboard/api/status", jsonHeaders)).toBe(false);
    expect(shouldInjectHermesDashboardPathShim("/gateway/", htmlHeaders)).toBe(false);
  });

  it("rewrites Hermes dashboard API HTTP, XHR, and WebSocket calls under the machine route", () => {
    const html = injectHermesDashboardPathShim("<html><head></head><body></body></html>");
    const script = html.match(/<script id="ocm-hermes-dashboard-path-shim">([\s\S]*?)<\/script>/)?.[1];
    expect(script).toBeTruthy();

    const fetchCalls = [];
    class TestRequest {
      constructor(url) {
        this.url = url;
      }
    }
    function NativeWebSocket(url, protocols) {
      this.url = url;
      this.protocols = protocols;
    }
    NativeWebSocket.CONNECTING = 0;
    NativeWebSocket.OPEN = 1;
    NativeWebSocket.CLOSING = 2;
    NativeWebSocket.CLOSED = 3;
    class NativeXMLHttpRequest {
      open(...args) {
        this.openArgs = args;
      }
    }

    const context = {
      URL,
      URLSearchParams,
      Request: TestRequest,
      XMLHttpRequest: NativeXMLHttpRequest,
      document: { referrer: "" },
      setTimeout() {},
      setInterval() {},
      window: {
        __HERMES_BASE_PATH__: "/my-machine/dashboard",
        location: {
          href: "https://acme.openclawmachines.com/my-machine/dashboard/",
          host: "acme.openclawmachines.com",
          pathname: "/my-machine/dashboard/",
        },
        fetch(input) {
          fetchCalls.push(input);
          return input;
        },
        WebSocket: NativeWebSocket,
      },
    };
    vm.runInNewContext(script, context);

    context.window.fetch("/api/status");
    expect(fetchCalls[0]).toBe("/my-machine/dashboard/api/status");

    const xhr = new context.XMLHttpRequest();
    xhr.open("GET", "/api/events");
    expect(xhr.openArgs[1]).toBe("/my-machine/dashboard/api/events");

    const ws = new context.window.WebSocket("wss://acme.openclawmachines.com/api/ws?token=abc");
    expect(ws.url).toBe("wss://acme.openclawmachines.com/my-machine/dashboard/api/ws?token=abc");
  });

  it("posts the latest Hermes session id to the embedding OCM page", async () => {
    const html = injectHermesDashboardPathShim("<html><head></head><body></body></html>");
    const script = html.match(/<script id="ocm-hermes-dashboard-path-shim">([\s\S]*?)<\/script>/)?.[1];
    expect(script).toBeTruthy();

    const posted = [];
    const fetchCalls = [];
    class TestRequest {
      constructor(url) {
        this.url = url;
      }
    }
    function NativeWebSocket(url, protocols) {
      this.url = url;
      this.protocols = protocols;
    }
    class NativeXMLHttpRequest {
      open(...args) {
        this.openArgs = args;
      }
    }

    const context = {
      URL,
      URLSearchParams,
      Request: TestRequest,
      XMLHttpRequest: NativeXMLHttpRequest,
      document: { referrer: "https://openclawmachines.com/chat/test-machine" },
      setTimeout(fn) {
        fn();
      },
      setInterval() {},
      window: {
        __HERMES_BASE_PATH__: "/my-machine/dashboard",
        __HERMES_SESSION_TOKEN__: "dashboard-token",
        parent: {
          postMessage(message, origin) {
            posted.push({ message, origin });
          },
        },
        location: {
          href: "https://acme.openclawmachines.com/my-machine/dashboard/chat",
          host: "acme.openclawmachines.com",
          pathname: "/my-machine/dashboard/chat",
          search: "",
        },
        fetch(input, init) {
          fetchCalls.push({ input, init });
          return Promise.resolve({
            ok: true,
            json: () => Promise.resolve({
              sessions: [
                { id: "older", last_active: 100 },
                { id: "latest", last_active: 200 },
              ],
            }),
          });
        },
        WebSocket: NativeWebSocket,
      },
    };
    context.window.parent.window = context.window.parent;

    vm.runInNewContext(script, context);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(fetchCalls[0].input).toBe("/my-machine/dashboard/api/sessions?limit=50");
    expect(fetchCalls[0].init.headers["X-Hermes-Session-Token"]).toBe("dashboard-token");
    expect(JSON.parse(JSON.stringify(posted))).toContainEqual({
      message: { type: "ocm.hermes.session", sessionId: "latest", lastActive: 200000 },
      origin: "https://openclawmachines.com",
    });
  });

  it("ignores newer empty Hermes sidecar sessions when posting resume id", async () => {
    const html = injectHermesDashboardPathShim("<html><head></head><body></body></html>");
    const script = html.match(/<script id="ocm-hermes-dashboard-path-shim">([\s\S]*?)<\/script>/)?.[1];
    expect(script).toBeTruthy();

    const posted = [];
    class TestRequest {
      constructor(url) {
        this.url = url;
      }
    }
    function NativeWebSocket(url, protocols) {
      this.url = url;
      this.protocols = protocols;
    }
    class NativeXMLHttpRequest {
      open(...args) {
        this.openArgs = args;
      }
    }

    const context = {
      URL,
      URLSearchParams,
      Request: TestRequest,
      XMLHttpRequest: NativeXMLHttpRequest,
      document: { referrer: "https://openclawmachines.com/chat/test-machine" },
      setTimeout(fn) {
        fn();
      },
      setInterval() {},
      window: {
        __HERMES_BASE_PATH__: "/my-machine/dashboard",
        __HERMES_SESSION_TOKEN__: "dashboard-token",
        parent: {
          postMessage(message, origin) {
            posted.push({ message, origin });
          },
        },
        location: {
          href: "https://acme.openclawmachines.com/my-machine/dashboard/chat",
          host: "acme.openclawmachines.com",
          pathname: "/my-machine/dashboard/chat",
          search: "",
        },
        fetch() {
          return Promise.resolve({
            ok: true,
            json: () => Promise.resolve({
              sessions: [
                { id: "empty-sidecar", last_active: 300, message_count: 0 },
                { id: "real-chat", last_active: 200, message_count: 4 },
              ],
            }),
          });
        },
        WebSocket: NativeWebSocket,
      },
    };

    vm.runInNewContext(script, context);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(JSON.parse(JSON.stringify(posted))).toContainEqual({
      message: { type: "ocm.hermes.session", sessionId: "real-chat", lastActive: 200000 },
      origin: "https://openclawmachines.com",
    });
  });
});
