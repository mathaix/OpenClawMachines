import puppeteer from "@cloudflare/puppeteer";

const BOT_PATTERNS = [
  "googlebot",
  "bingbot",
  "slurp",
  "duckduckbot",
  "baiduspider",
  "yandexbot",
  "linkedinbot",
  "twitterbot",
  "facebookexternalhit",
  "whatsapp",
  "slackbot",
  "discordbot",
  "telegrambot",
  "applebot",
];

/**
 * Check if User-Agent is a known bot/crawler.
 */
export function isBot(userAgent) {
  if (!userAgent) return false;
  const ua = userAgent.toLowerCase();
  return BOT_PATTERNS.some((p) => ua.includes(p));
}

// Only pre-render public marketing pages, not API or dashboard routes.
const PRERENDER_PATHS = ["/", "/pricing", "/blog"];

/**
 * Check if a path should be pre-rendered for bots.
 */
export function shouldPrerender(pathname) {
  if (PRERENDER_PATHS.includes(pathname)) return true;
  if (pathname.startsWith("/blog/")) return true;
  return false;
}

const CACHE_TTL = 86400; // 24 hours

/**
 * Get pre-rendered HTML for a URL. Uses KV cache, falls back to Browser Rendering.
 * Returns null if rendering fails (caller should fall through to normal response).
 */
export async function getPrerenderedHTML(env, requestUrl, ctx) {
  // Normalize URL: strip query params to prevent cache poisoning / exhaustion
  const urlObj = new URL(requestUrl);
  urlObj.search = "";
  const normalizedUrl = urlObj.toString();

  const cacheKey = `seo:${normalizedUrl}`;

  // Try KV cache first
  if (env.SEO_CACHE) {
    const cached = await env.SEO_CACHE.get(cacheKey);
    if (cached) return cached;
  }

  // Cache miss — render with Browser Rendering
  if (!env.BROWSER) return null;

  let browser = null;
  try {
    browser = await puppeteer.launch(env.BROWSER);
    const page = await browser.newPage();

    await page.goto(normalizedUrl, {
      waitUntil: "networkidle2",
      timeout: 15000,
    });

    const html = await page.content();
    await page.close();

    // Cache in KV (fire and forget)
    if (env.SEO_CACHE && html) {
      ctx.waitUntil(
        env.SEO_CACHE.put(cacheKey, html, { expirationTtl: CACHE_TTL }).catch(() => {})
      );
    }

    return html;
  } catch (err) {
    console.error("Prerender failed:", err.message || err);
    return null;
  } finally {
    if (browser) {
      try { await browser.close(); } catch { /* ignore */ }
    }
  }
}
