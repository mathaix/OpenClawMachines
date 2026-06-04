// Local HTTP + WebSocket mock server
// Simulates: backend /api/internal/resolve + agent /proxy/* endpoints

import http from "node:http";
import { WebSocketServer } from "ws";

let server;
let wss;
let resolveHandler = defaultResolveHandler;
let agentHandler = defaultAgentHandler;
let lastAgentRequest = null;

function defaultResolveHandler(body) {
  if (body.account_slug === "acme" && body.machine_slug === "my-machine") {
    return {
      machine_id: "vm-abc123",
      host_hostname: `127.0.0.1:${server.address().port}`,
      proxy_token: "test-proxy-token",
      account_id: "acct-001",
      user_ids: ["user-123"],
    };
  }
  return null;
}

function defaultAgentHandler(req, res) {
  lastAgentRequest = {
    method: req.method,
    url: req.url,
    headers: { ...req.headers },
  };
  res.writeHead(200, { "Content-Type": "application/json" });
  res.end(
    JSON.stringify({
      method: req.method,
      url: req.url,
      headers: { ...req.headers },
    })
  );
}

export function setResolveHandler(fn) {
  resolveHandler = fn;
}
export function setAgentHandler(fn) {
  agentHandler = fn;
}
export function resetHandlers() {
  resolveHandler = defaultResolveHandler;
  agentHandler = defaultAgentHandler;
  lastAgentRequest = null;
}
export function getLastAgentRequest() {
  return lastAgentRequest;
}

export async function startMockServer() {
  return new Promise((resolve) => {
    server = http.createServer((req, res) => {
      // Backend resolve endpoint
      if (req.url === "/api/internal/resolve" && req.method === "POST") {
        let body = "";
        req.on("data", (chunk) => (body += chunk));
        req.on("end", () => {
          try {
            const parsed = JSON.parse(body);
            const result = resolveHandler(parsed);
            if (result) {
              res.writeHead(200, { "Content-Type": "application/json" });
              res.end(JSON.stringify(result));
            } else {
              res.writeHead(404);
              res.end("Not found");
            }
          } catch {
            res.writeHead(400);
            res.end("Bad request");
          }
        });
        return;
      }

      // Agent proxy endpoint
      if (req.url.startsWith("/proxy/")) {
        agentHandler(req, res);
        return;
      }

      res.writeHead(404);
      res.end("Mock server: unknown route");
    });

    // WebSocket upgrade handler for agent WS proxy tests
    wss = new WebSocketServer({ server });
    wss.on("connection", (ws, req) => {
      // Echo back messages with "echo:" prefix
      ws.on("message", (msg) => {
        ws.send(`echo:${msg.toString()}`);
      });
    });

    const port = parseInt(process.env.MOCK_SERVER_PORT || "54321", 10);
    server.listen(port, "127.0.0.1", () => {
      const addr = server.address();
      resolve({
        url: `http://${addr.address}:${addr.port}`,
        port: addr.port,
        server,
      });
    });
  });
}

export async function stopMockServer(mockServer) {
  return new Promise((resolve) => {
    wss?.close();
    mockServer?.server?.close(() => resolve());
  });
}
