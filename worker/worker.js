// OCM Worker — Host routing + Subdomain routing via KV
// VERSION is injected at deploy time via wrangler --var flag.
import { verifyRequestJWT } from "./jwt.js";
import { isBot, shouldPrerender, getPrerenderedHTML } from "./prerender.js";

// ---- HMAC signing helpers for KV payloads ----

/**
 * Compute HMAC-SHA256 signature of data using the given key.
 * Returns base64-encoded signature string.
 */
async function hmacSign(key, data) {
  const encoder = new TextEncoder();
  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    encoder.encode(key),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"]
  );
  const sig = await crypto.subtle.sign("HMAC", cryptoKey, encoder.encode(data));
  return btoa(String.fromCharCode(...new Uint8Array(sig)));
}

/**
 * Verify HMAC-SHA256 signature using constant-time comparison.
 */
async function hmacVerify(key, data, signature) {
  const computed = await hmacSign(key, data);
  if (computed.length !== signature.length) return false;
  let result = 0;
  for (let i = 0; i < computed.length; i++) {
    result |= computed.charCodeAt(i) ^ signature.charCodeAt(i);
  }
  return result === 0;
}

/**
 * Write a signed payload to KV. If KV_SIGNING_KEY is not set, falls back to unsigned.
 */
async function putSignedKV(env, key, data, ttl) {
  const payload = JSON.stringify(data);
  if (env.KV_SIGNING_KEY) {
    const sig = await hmacSign(env.KV_SIGNING_KEY, payload);
    await env.OCM_ROUTES.put(key, JSON.stringify({ p: payload, s: sig }), { expirationTtl: ttl });
  } else {
    await env.OCM_ROUTES.put(key, payload, { expirationTtl: ttl });
  }
}

/**
 * Read and verify a signed payload from KV. Returns parsed data or null.
 * If KV_SIGNING_KEY is not set, falls back to unsigned reads (backward compat).
 */
async function getSignedKV(env, key) {
  const raw = await env.OCM_ROUTES.get(key);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    // If signing is enabled and the payload has signature wrapper, verify it
    if (env.KV_SIGNING_KEY && parsed.p !== undefined && parsed.s !== undefined) {
      if (!await hmacVerify(env.KV_SIGNING_KEY, parsed.p, parsed.s)) {
        console.error("KV HMAC verification failed for key:", key);
        return null; // Treat as cache miss
      }
      return JSON.parse(parsed.p);
    }
    // No signing key or legacy unsigned entry — return as-is
    return parsed;
  } catch {
    return null; // Treat corrupted data as cache miss
  }
}

const DEFAULT_BASE_DOMAIN = "openclawmachines.com";

function getBaseDomain(env = {}) {
  const domain = (env.BASE_DOMAIN || "").trim().toLowerCase().replace(/^\.+|\.+$/g, "");
  return domain || DEFAULT_BASE_DOMAIN;
}

function staticOriginForHost(env, hostname) {
  const baseDomain = getBaseDomain(env);
  const frontendOrigin = env.FRONTEND_ORIGIN_HOST || "";
  const backendOrigin = env.BACKEND_ORIGIN_HOST || "";
  if (frontendOrigin && (hostname === baseDomain || hostname === `www.${baseDomain}` || hostname === `app.${baseDomain}`)) {
    return frontendOrigin;
  }
  if (backendOrigin && hostname === `api.${baseDomain}`) {
    return backendOrigin;
  }
  return null;
}

/**
 * Create CORS headers for subdomain routing.
 * Allows cross-origin requests between the operator data-plane subdomains.
 */
function isAllowedOrigin(origin, baseDomain = DEFAULT_BASE_DOMAIN) {
  if (!origin) return false;
  if (origin === `https://${baseDomain}`) return true;
  if (origin === `https://www.${baseDomain}`) return true;
  return origin.startsWith("https://") && origin.endsWith(`.${baseDomain}`);
}

function getCorsHeaders(request, baseDomain = DEFAULT_BASE_DOMAIN) {
  const origin = request.headers.get("Origin");
  if (isAllowedOrigin(origin, baseDomain)) {
    return {
      "Access-Control-Allow-Origin": origin,
      "Access-Control-Allow-Credentials": "true",
      "Access-Control-Allow-Methods": "GET, POST, PUT, DELETE, OPTIONS",
      "Access-Control-Allow-Headers": "Content-Type, Authorization, X-Requested-With, Cf-Access-Jwt-Assertion",
      "Access-Control-Max-Age": "86400",
    };
  }
  return null;
}

/**
 * Handle CORS preflight requests.
 */
function handleOptions(request, baseDomain = DEFAULT_BASE_DOMAIN) {
  const corsHeaders = getCorsHeaders(request, baseDomain);
  if (corsHeaders) {
    return new Response(null, { status: 204, headers: corsHeaders });
  }
  return new Response(null, { status: 403 });
}

/**
 * Add CORS headers to a response.
 */
function addCorsHeaders(response, request, baseDomain = DEFAULT_BASE_DOMAIN) {
  const corsHeaders = getCorsHeaders(request, baseDomain);
  if (!corsHeaders) {
    return response;
  }
  const newHeaders = new Headers(response.headers);
  for (const [key, value] of Object.entries(corsHeaders)) {
    newHeaders.set(key, value);
  }
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: newHeaders,
  });
}

/**
 * Extract account slug from hostname.
 * E.g. "myteam.example.com" → "myteam"
 * Returns null for bare domain or known subdomains (www, api, app).
 */
function extractAccountSlug(hostname, baseDomain = DEFAULT_BASE_DOMAIN) {
  if (!hostname.endsWith("." + baseDomain)) return null;
  const sub = hostname.slice(0, -(baseDomain.length + 1));
  // Single-level subdomain only (no dots)
  if (!sub || sub.includes(".")) return null;
  // Exclude known subdomains and host tunnel hostnames
  if (sub === "www" || sub === "api" || sub === "app") return null;
  if (sub.startsWith("ocm-host-")) return null;
  // Per-VM tunnel hostnames — let the tunnel handle these
  if (sub.startsWith("m-") || sub.startsWith("ssh-")) return null;
  return sub;
}

/**
 * Extract machine slug and remaining path from pathname.
 * E.g. "/my-machine/files?path=/" → { machineSlug: "my-machine", subPath: "/files?path=/" }
 * Returns null if no machine slug present.
 */
function extractMachinePath(pathname) {
  // Remove leading slash, split on first /
  const withoutLeading = pathname.startsWith("/") ? pathname.slice(1) : pathname;
  if (!withoutLeading) return null;
  const slashIdx = withoutLeading.indexOf("/");
  if (slashIdx === -1) {
    return { machineSlug: withoutLeading, subPath: "/" };
  }
  return {
    machineSlug: withoutLeading.slice(0, slashIdx),
    subPath: "/" + withoutLeading.slice(slashIdx + 1),
  };
}

/**
 * Proxy a WebSocket connection through the Worker using WebSocketPair.
 * Cloudflare Workers require explicit message piping for WebSocket proxying.
 */
async function proxyWebSocket(request, url, route, machineSlug, subPath, env) {
  const targetUrl = new URL(`/proxy/${route.machine_id}${subPath}`, agentOriginForHostHostname(route.host_hostname, env));
  targetUrl.search = url.search;

  const headers = new Headers(request.headers);
  headers.set("X-Proxy-Token", route.proxy_token || "");
  const dashboardBasePath = hermesDashboardBasePath(machineSlug, subPath);
  if (dashboardBasePath) {
    headers.set("X-Forwarded-Prefix", dashboardBasePath);
  }
  headers.delete("Cookie");
  if (env.CF_SERVICE_TOKEN_ID) {
    headers.set("CF-Access-Client-Id", env.CF_SERVICE_TOKEN_ID);
    headers.set("CF-Access-Client-Secret", env.CF_SERVICE_TOKEN_SECRET);
  }

  // Connect to origin
  const originResponse = await fetch(targetUrl.toString(), {
    method: "GET",
    headers,
  });

  if (!originResponse.webSocket) {
    const body = await originResponse.text().catch(() => "");
    return new Response("WebSocket upgrade failed upstream: " + body, {
      status: originResponse.status || 502,
    });
  }

  // Create client-facing WebSocket pair
  const [client, server] = Object.values(new WebSocketPair());
  server.accept();
  const origin = originResponse.webSocket;
  origin.accept();

  // Helper: CF Workers only allow close codes 1000 and 3000-4999.
  // Map other codes (e.g. 1008 from gateway) to 4000-range so the browser
  // receives a meaningful close instead of a silent 1006.
  function safeCloseCode(code) {
    if (code === 1000 || (code >= 3000 && code <= 4999)) return code;
    return 4000 + (code % 1000);
  }

  // Pipe: origin → client
  origin.addEventListener("message", (event) => {
    try { server.send(event.data); } catch { /* ignore */ }
  });
  origin.addEventListener("close", (event) => {
    try { server.close(safeCloseCode(event.code), event.reason || ""); } catch { /* ignore */ }
  });
  origin.addEventListener("error", () => {});

  // Pipe: client → origin
  server.addEventListener("message", (event) => {
    try { origin.send(event.data); } catch { /* ignore */ }
  });
  server.addEventListener("close", (event) => {
    try { origin.close(safeCloseCode(event.code), event.reason || ""); } catch { /* ignore */ }
  });
  server.addEventListener("error", () => {});

  return new Response(null, { status: 101, webSocket: client });
}

function hermesDashboardBasePath(machineSlug, subPath) {
  if (!machineSlug) return "";
  if (subPath !== "/dashboard" && !subPath.startsWith("/dashboard/")) return "";
  return `/${machineSlug}/dashboard`;
}

function jsonForInlineScript(value) {
  return JSON.stringify(value).replace(/</g, "\\u003c");
}

function hermesDashboardPathShim(basePath = "", baseDomain = DEFAULT_BASE_DOMAIN) {
  return `<script id="ocm-hermes-dashboard-path-shim">
(function(){
  if (window.__OCM_HERMES_DASHBOARD_PATH_SHIM__) return;
  window.__OCM_HERMES_DASHBOARD_PATH_SHIM__ = true;
  var OCM_BASE_PATH = ${jsonForInlineScript(basePath || "")};
  var OCM_BASE_DOMAIN = ${jsonForInlineScript(baseDomain || DEFAULT_BASE_DOMAIN)};
  if (OCM_BASE_PATH) window.__HERMES_BASE_PATH__ = OCM_BASE_PATH;
  var SESSION_POLL_MS = 3000;
  function basePath(){
    if (window.__HERMES_BASE_PATH__) return window.__HERMES_BASE_PATH__;
    if (OCM_BASE_PATH) return OCM_BASE_PATH;
    var parts = window.location.pathname.split("/").filter(Boolean);
    return parts.length >= 2 ? "/" + parts[0] + "/" + parts[1] : "";
  }
  function rewrite(value){
    var base = basePath();
    if (!base || !value) return value;
    try {
      var s = String(value);
      if (s === "/api" || s.indexOf("/api/") === 0) return base + s;
      var u = new URL(s, window.location.href);
      if (u.host === window.location.host && (u.pathname === "/api" || u.pathname.indexOf("/api/") === 0)) {
        u.pathname = base + u.pathname;
        return u.toString();
      }
    } catch (_) {}
    return value;
  }
  var nativeFetch = window.fetch;
  window.fetch = function(input, init){
    if (input instanceof Request) {
      var next = rewrite(input.url);
      if (next !== input.url) input = new Request(next, input);
    } else {
      input = rewrite(input);
    }
    return nativeFetch.call(this, input, init);
  };
  var NativeWebSocket = window.WebSocket;
  window.WebSocket = function(url, protocols){
    return protocols === undefined ? new NativeWebSocket(rewrite(url)) : new NativeWebSocket(rewrite(url), protocols);
  };
  window.WebSocket.prototype = NativeWebSocket.prototype;
  Object.defineProperty(window.WebSocket, "OPEN", { value: NativeWebSocket.OPEN });
  Object.defineProperty(window.WebSocket, "CONNECTING", { value: NativeWebSocket.CONNECTING });
  Object.defineProperty(window.WebSocket, "CLOSING", { value: NativeWebSocket.CLOSING });
  Object.defineProperty(window.WebSocket, "CLOSED", { value: NativeWebSocket.CLOSED });
  var nativeOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(method, url){
    arguments[1] = rewrite(url);
    return nativeOpen.apply(this, arguments);
  };
  function allowedParentOrigin(){
    try {
      if (!document.referrer) return null;
      var u = new URL(document.referrer);
      var host = u.hostname;
      if (u.protocol !== "https:" && host !== "localhost" && host !== "127.0.0.1") return null;
      if (host === OCM_BASE_DOMAIN || host === "www." + OCM_BASE_DOMAIN || host === "app." + OCM_BASE_DOMAIN || host.endsWith("." + OCM_BASE_DOMAIN) || host === "localhost" || host === "127.0.0.1") {
        return u.origin;
      }
    } catch (_) {}
    return null;
  }
  var parentOrigin = allowedParentOrigin();
  function postSession(id, lastActive){
    if (!id || !parentOrigin || window.parent === window) return;
    window.parent.postMessage({ type: "ocm.hermes.session", sessionId: String(id), lastActive: lastActive || null }, parentOrigin);
  }
  function numericField(obj, keys){
    for (var i = 0; i < keys.length; i++) {
      var value = obj && obj[keys[i]];
      if (typeof value === "number" && Number.isFinite(value)) return value < 1000000000000 ? value * 1000 : value;
      if (typeof value === "string" && value) {
        var parsed = Date.parse(value);
        if (Number.isFinite(parsed)) return parsed;
      }
    }
    return 0;
  }
  function pickSession(payload){
    var sessions = Array.isArray(payload) ? payload : payload && payload.sessions;
    if (!Array.isArray(sessions) || sessions.length === 0) return null;
    sessions = sessions.slice().filter(function(s){ return s && typeof s === "object" && s.id; });
    var withMessages = sessions.filter(function(s){
      var count = s.message_count;
      return typeof count === "number" ? count > 0 : Number(count || 0) > 0;
    });
    if (withMessages.length > 0) sessions = withMessages;
    sessions.sort(function(a, b){
      return numericField(b, ["last_active", "updated_at", "updatedAt", "started_at", "startedAt", "created_at", "createdAt"]) -
        numericField(a, ["last_active", "updated_at", "updatedAt", "started_at", "startedAt", "created_at", "createdAt"]);
    });
    return sessions[0] || null;
  }
  function publishLatestSession(){
    try {
      var params = new URLSearchParams(window.location.search);
      var resume = params.get("resume");
      if (resume) postSession(resume, Date.now());
    } catch (_) {}
    var token = window.__HERMES_SESSION_TOKEN__;
    if (!token || !parentOrigin) return;
    window.fetch("/api/sessions?limit=50", {
      cache: "no-store",
      headers: { "X-Hermes-Session-Token": token }
    }).then(function(res){
      if (!res.ok) return null;
      return res.json();
    }).then(function(payload){
      var session = pickSession(payload);
      if (session && session.id) {
        postSession(session.id, numericField(session, ["last_active", "updated_at", "updatedAt", "started_at", "startedAt", "created_at", "createdAt"]));
      }
    }).catch(function(){});
  }
  if (parentOrigin) {
    setTimeout(publishLatestSession, 1000);
    setInterval(publishLatestSession, SESSION_POLL_MS);
  }
})();
</script>`;
}

function prefixHermesDashboardAssetPaths(html, basePath = "") {
  if (!basePath) return html;
  let out = html;
  const rootPrefixes = [
    "/assets/",
    "/fonts/",
    "/fonts-terminal/",
    "/ds-assets/",
    "/dashboard-plugins/",
  ];
  for (const prefix of rootPrefixes) {
    for (const attr of ["href", "src"]) {
      out = out.split(`${attr}="${prefix}`).join(`${attr}="${basePath}${prefix}`);
      out = out.split(`${attr}='${prefix}`).join(`${attr}='${basePath}${prefix}`);
    }
  }
  out = out.split('href="/favicon.ico"').join(`href="${basePath}/favicon.ico"`);
  out = out.split("href='/favicon.ico'").join(`href='${basePath}/favicon.ico'`);
  return out;
}

function injectHermesDashboardPathShim(html, basePath = "", baseDomain = DEFAULT_BASE_DOMAIN) {
  const rewritten = prefixHermesDashboardAssetPaths(html, basePath);
  if (rewritten.includes("__OCM_HERMES_DASHBOARD_PATH_SHIM__")) return rewritten;
  const shim = hermesDashboardPathShim(basePath, baseDomain);
  const headClose = rewritten.indexOf("</head>");
  if (headClose >= 0) {
    return rewritten.slice(0, headClose) + shim + rewritten.slice(headClose);
  }
  return shim + rewritten;
}

function shouldInjectHermesDashboardPathShim(subPath, responseHeaders) {
  if (subPath !== "/dashboard" && !subPath.startsWith("/dashboard/")) return false;
  const contentType = responseHeaders.get("Content-Type") || "";
  return contentType.toLowerCase().includes("text/html");
}

function frameAncestorsDirective(baseDomain = DEFAULT_BASE_DOMAIN) {
  const domain = (baseDomain || DEFAULT_BASE_DOMAIN).trim().replace(/^\.+|\.+$/g, "");
  if (!domain || domain === "localhost" || domain === "127.0.0.1") {
    return "frame-ancestors 'self'";
  }
  return `frame-ancestors 'self' ${domain} *.${domain}`;
}

function isLocalAgentHostname(hostname) {
  const h = (hostname || "").toLowerCase();
  return h === "localhost" || h === "127.0.0.1" || h === "::1";
}

function agentOriginForHostHostname(hostHostname, env = {}) {
  const raw = (hostHostname || "").trim();
  if (!raw) {
    throw new Error("host hostname is required");
  }

  const origin = new URL(/^https?:\/\//i.test(raw) ? raw : `https://${raw}`);
  if (env.ALLOW_INSECURE_LOCAL_AGENT_ORIGIN === "1" && isLocalAgentHostname(origin.hostname)) {
    origin.protocol = "http:";
  } else {
    origin.protocol = "https:";
  }
  origin.pathname = "/";
  origin.search = "";
  origin.hash = "";
  return origin;
}

/**
 * Forward request to host agent proxy via Cloudflare Tunnel.
 */
async function forwardToAgent(request, url, hostHostname, machineID, proxyToken, machineSlug, subPath, env) {
  const baseDomain = getBaseDomain(env);
  const agentUrl = new URL(`/proxy/${machineID}${subPath}`, agentOriginForHostHostname(hostHostname, env));
  agentUrl.search = url.search;

  const headers = new Headers(request.headers);
  headers.set("X-Proxy-Token", proxyToken);
  const dashboardBasePath = hermesDashboardBasePath(machineSlug, subPath);
  if (dashboardBasePath) {
    headers.set("X-Forwarded-Prefix", dashboardBasePath);
  }
  // Strip cookies — agent doesn't need user cookies
  headers.delete("Cookie");
  // Add Cloudflare Access service token for tunnel authentication
  if (env.CF_SERVICE_TOKEN_ID) {
    headers.set("CF-Access-Client-Id", env.CF_SERVICE_TOKEN_ID);
    headers.set("CF-Access-Client-Secret", env.CF_SERVICE_TOKEN_SECRET);
  }

  const fetchOptions = {
    method: request.method,
    headers,
  };

  // Only include body for non-WebSocket requests
  if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
    fetchOptions.body = request.body;
  }

  const resp = await fetch(agentUrl.toString(), fetchOptions);

  // Cloudflare Access injects X-Frame-Options: SAMEORIGIN on tunnel responses.
  // Strip iframe-blocking headers so the gateway can be embedded in our dashboard.
  const cleanHeaders = new Headers(resp.headers);
  cleanHeaders.delete("X-Frame-Options");
  // Rewrite frame-ancestors 'none' to allow our domain
  const csp = cleanHeaders.get("Content-Security-Policy");
  if (csp && csp.includes("frame-ancestors 'none'")) {
    cleanHeaders.set("Content-Security-Policy",
      csp.replace("frame-ancestors 'none'", frameAncestorsDirective(baseDomain)));
  }

  if (shouldInjectHermesDashboardPathShim(subPath, cleanHeaders)) {
    const html = await resp.text();
    cleanHeaders.delete("Content-Length");
    cleanHeaders.delete("Content-Encoding");
    return new Response(injectHermesDashboardPathShim(html, dashboardBasePath, baseDomain), {
      status: resp.status,
      statusText: resp.statusText,
      headers: cleanHeaders,
    });
  }

  return new Response(resp.body, {
    status: resp.status,
    statusText: resp.statusText,
    headers: cleanHeaders,
  });
}

/**
 * Resolve route via /internal/resolve fallback endpoint.
 */
async function resolveViaBackend(env, accountSlug, machineSlug) {
  // Allow override for testing — env.RESOLVE_ORIGIN is a full URL like "http://127.0.0.1:54321"
  let resolveUrl;
  let hostHeader;
  if (env.RESOLVE_ORIGIN) {
    resolveUrl = `${env.RESOLVE_ORIGIN}/api/internal/resolve`;
    hostHeader = new URL(env.RESOLVE_ORIGIN).host;
  } else {
    const backendOrigin = staticOriginForHost(env, `api.${getBaseDomain(env)}`);
    if (!backendOrigin) return null;
    resolveUrl = `https://${backendOrigin}/api/internal/resolve`;
    hostHeader = backendOrigin;
  }

  const headers = new Headers({
    "Host": hostHeader,
    "Content-Type": "application/json",
  });
  if (env.CF_SERVICE_TOKEN_ID) {
    headers.set("CF-Access-Client-Id", env.CF_SERVICE_TOKEN_ID);
    headers.set("CF-Access-Client-Secret", env.CF_SERVICE_TOKEN_SECRET);
  }

  try {
    const resp = await fetch(resolveUrl, {
      method: "POST",
      headers,
      body: JSON.stringify({
        account_slug: accountSlug,
        machine_slug: machineSlug,
      }),
    });
    if (!resp.ok) return null;
    return await resp.json();
  } catch {
    return null;
  }
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const baseDomain = getBaseDomain(env);

    // Redirect www to bare domain so edge auth protection applies consistently.
    if (url.hostname === "www." + baseDomain) {
      url.hostname = baseDomain;
      return Response.redirect(url.toString(), 301);
    }

    // Health check endpoint — returns version info
    if (url.pathname === "/__version") {
      return new Response(
        JSON.stringify({ version: env.VERSION }),
        { headers: { "Content-Type": "application/json" } }
      );
    }

    // Temporary auth debug endpoint — shows what headers the Worker receives
    // Available at both /__auth-debug (unprotected) and /api/auth/__debug (CF Access protected)
    if (url.pathname === "/__auth-debug" || url.pathname === "/api/auth/__debug") {
      const cfJwt = request.headers.get("Cf-Access-Jwt-Assertion") || "";
      const cookie = request.headers.get("Cookie") || "";
      const hasCfCookie = cookie.includes("CF_Authorization");
      // Extract cookie names (not values) for debugging
      const cookieNames = cookie.split(";").map(c => c.trim().split("=")[0]).filter(Boolean);
      // Check for all CF-related headers
      const cfHeaders = {};
      for (const [key, value] of request.headers.entries()) {
        if (key.toLowerCase().startsWith("cf-") || key.toLowerCase().includes("access")) {
          cfHeaders[key] = key.toLowerCase().includes("jwt") || key.toLowerCase().includes("secret")
            ? `(len=${value.length})` : value;
        }
      }
      return new Response(
        JSON.stringify({
          cf_jwt_header_present: cfJwt.length > 0,
          cf_jwt_header_len: cfJwt.length,
          cookie_names: cookieNames,
          has_cf_authorization_cookie: hasCfCookie,
          cf_headers: cfHeaders,
          hostname: url.hostname,
        }, null, 2),
        { headers: { "Content-Type": "application/json" } }
      );
    }

    // --- SEO bot pre-rendering ---
    // Serve pre-rendered HTML to search engine bots on public frontend pages.
    const userAgent = request.headers.get("User-Agent") || "";
    if (
      isBot(userAgent) &&
      (url.hostname === baseDomain || url.hostname === "www." + baseDomain) &&
      shouldPrerender(url.pathname)
    ) {
      try {
        const html = await getPrerenderedHTML(env, request.url, ctx);
        if (html) {
          return new Response(html, {
            headers: {
              "Content-Type": "text/html;charset=UTF-8",
              "X-Prerendered": "true",
              "Cache-Control": "public, max-age=3600",
            },
          });
        }
      } catch {
        // Fall through to normal response if pre-rendering fails
      }
    }

    // --- Static host routing (existing behavior) ---
    let staticOrigin = staticOriginForHost(env, url.hostname);

    // Route /api/* on any frontend domain to the backend origin.
    // This keeps API requests same-origin so CF Access handles auth at the edge
    // and the browser sends CF_Authorization cookie automatically — no JS cookie
    // reading needed (fixes Safari ITP blocking cross-origin cookie access).
    const isFrontendHost = url.hostname === baseDomain
      || url.hostname === "www." + baseDomain
      || url.hostname === "app." + baseDomain;
    if (staticOrigin && isFrontendHost && url.pathname.startsWith("/api/")) {
      staticOrigin = staticOriginForHost(env, "api." + baseDomain);
    }

    if (staticOrigin) {
      const originUrl = new URL(request.url);
      originUrl.hostname = staticOrigin;
      originUrl.port = "443";
      originUrl.protocol = "https:";

      const modifiedHeaders = new Headers(request.headers);
      modifiedHeaders.set("Host", staticOrigin);
      modifiedHeaders.set("X-Forwarded-Host", url.hostname);

      // CF Access adds Cf-Access-Jwt-Assertion header on protected paths, but
      // Cloudflare strips it from outbound Worker fetch() requests (security
      // measure). Forward the JWT via X-Cf-Access-Jwt which won't be stripped.
      const cfJwtHeader = request.headers.get("Cf-Access-Jwt-Assertion") || "";
      const isFrontendApi = isFrontendHost && url.pathname.startsWith("/api/");

      if (cfJwtHeader && isFrontendApi) {
        modifiedHeaders.set("X-Cf-Access-Jwt", cfJwtHeader);
      }

      // Fallback: if no CF JWT header (unprotected path), try CF_Authorization cookie
      if (!cfJwtHeader && isFrontendApi) {
        const cookieHeader = request.headers.get("Cookie") || "";
        const match = cookieHeader.match(/CF_Authorization=([^;]+)/);
        if (match) {
          modifiedHeaders.set("X-Cf-Access-Jwt", match[1]);
        }
      }

      const newRequest = new Request(originUrl.toString(), {
        method: request.method,
        headers: modifiedHeaders,
        body: request.body,
        redirect: "follow",
      });

      const response = await fetch(newRequest);

      return response;
    }

    // --- Subdomain routing ---
    const accountSlug = extractAccountSlug(url.hostname, baseDomain);
    if (!accountSlug) {
      // Per-VM tunnel hostnames (m-*, ssh-*) — pass through to Cloudflare Tunnel
      const sub = url.hostname.endsWith("." + baseDomain)
        ? url.hostname.slice(0, -(baseDomain.length + 1))
        : null;
      if (sub && (sub.startsWith("m-") || sub.startsWith("ssh-"))) {
        return fetch(request);
      }
      return new Response("Unknown host", { status: 404 });
    }

    // Handle CORS preflight
    if (request.method === "OPTIONS") {
      return handleOptions(request, baseDomain);
    }

    const machineInfo = extractMachinePath(url.pathname);
    if (!machineInfo) {
      return addCorsHeaders(new Response("Not found — specify a machine path", { status: 404 }), request, baseDomain);
    }

    const { machineSlug, subPath } = machineInfo;

    // Verify JWT from cookie
    if (!env.JWT_SECRET) {
      return addCorsHeaders(new Response("Server misconfiguration", { status: 500 }), request, baseDomain);
    }

    const claims = await verifyRequestJWT(request, env.JWT_SECRET);
    if (!claims) {
      return addCorsHeaders(new Response("Unauthorized", { status: 401 }), request, baseDomain);
    }

    // Look up account in KV (with HMAC verification if signing key is set)
    let accountEntry = null;
    if (env.OCM_ROUTES) {
      accountEntry = await getSignedKV(env, `account:${accountSlug}`);
      if (accountEntry) {
        // Verify user has access to this account
        if (accountEntry.user_ids && !accountEntry.user_ids.includes(claims.user_id)) {
          return addCorsHeaders(new Response("Forbidden", { status: 403 }), request, baseDomain);
        }
      }
    }

    // Look up route in KV (with HMAC verification if signing key is set)
    let route = null;
    if (env.OCM_ROUTES) {
      route = await getSignedKV(env, `route:${accountSlug}:${machineSlug}`);
    }

    // KV miss — fallback to /internal/resolve
    if (!route) {
      const resolved = await resolveViaBackend(env, accountSlug, machineSlug);
      if (!resolved) {
        return addCorsHeaders(new Response("Machine not found or not running", { status: 404 }), request, baseDomain);
      }

      // Verify user access from resolved data
      if (resolved.user_ids && !resolved.user_ids.includes(claims.user_id)) {
        return addCorsHeaders(new Response("Forbidden", { status: 403 }), request, baseDomain);
      }

      route = resolved;

      // Self-heal KV (fire and forget).
      // Use long TTLs (1h routes, 6h accounts) to minimise KV writes.
      // Free tier allows only 1000 puts/day but 100k reads/day.
      if (env.OCM_ROUTES) {
        const kvPromises = [];
        kvPromises.push(
          putSignedKV(env, `route:${accountSlug}:${machineSlug}`, {
            machine_id: resolved.machine_id,
            host_hostname: resolved.host_hostname,
            proxy_token: resolved.proxy_token,
          }, 3600)
        );
        if (resolved.account_id && !accountEntry) {
          kvPromises.push(
            putSignedKV(env, `account:${accountSlug}`, {
              account_id: resolved.account_id,
              user_ids: resolved.user_ids || [],
            }, 21600)
          );
        }
        ctx.waitUntil(Promise.all(kvPromises).catch(() => {}));
      }
    }

    // Forward to host agent
    if (!route.host_hostname || !route.machine_id) {
      return addCorsHeaders(new Response("Machine not running", { status: 502 }), request, baseDomain);
    }

    // WebSocket: use explicit bidirectional proxy via WebSocketPair
    if (request.headers.get("Upgrade")?.toLowerCase() === "websocket") {
      try {
        return await proxyWebSocket(request, url, route, machineSlug, subPath, env);
      } catch (err) {
        return new Response("WebSocket proxy error: upstream unreachable", { status: 502 });
      }
    }

    try {
      const agentResponse = await forwardToAgent(request, url, route.host_hostname, route.machine_id, route.proxy_token, machineSlug, subPath, env);
      return addCorsHeaders(agentResponse, request, baseDomain);
    } catch (err) {
      return addCorsHeaders(new Response("Upstream unreachable", { status: 502 }), request, baseDomain);
    }
  },
};

// Named exports for unit testing
export {
  extractAccountSlug,
  extractMachinePath,
  isAllowedOrigin,
  getCorsHeaders,
  hmacSign,
  hmacVerify,
  putSignedKV,
  getSignedKV,
  getBaseDomain,
  frameAncestorsDirective,
  agentOriginForHostHostname,
  hermesDashboardBasePath,
  hermesDashboardPathShim,
  injectHermesDashboardPathShim,
  prefixHermesDashboardAssetPaths,
  shouldInjectHermesDashboardPathShim,
};
