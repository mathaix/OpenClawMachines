// Render logo SVG candidates to contact-sheet PNGs for judging.
// Usage: node docs/branding/render.mjs <svg-file>... (run from frontend/ so playwright resolves)
// Output: <svg-dir>/<name>.render.png — 512px light+dark, 64px, 16px rows.
import { readFileSync } from "fs";
import { resolve, dirname, basename } from "path";
import { pathToFileURL } from "url";

// Resolve playwright from the frontend workspace regardless of cwd.
const root = resolve(dirname(new URL(import.meta.url).pathname), "../..");
const { chromium } = await import(
  pathToFileURL(resolve(root, "frontend/node_modules/playwright/index.mjs")).href
);

const files = process.argv.slice(2);
if (!files.length) {
  console.error("usage: node render.mjs <svg>...");
  process.exit(1);
}

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1180, height: 760 } });

for (const f of files) {
  const svg = readFileSync(f, "utf8");
  const enc = `data:image/svg+xml;base64,${Buffer.from(svg).toString("base64")}`;
  const cell = (size, bg, label) => `
    <div style="display:flex;flex-direction:column;align-items:center;gap:6px">
      <div style="width:${size}px;height:${size}px;background:${bg};display:flex;align-items:center;justify-content:center;border:1px solid #ccc;border-radius:8px">
        <img src="${enc}" style="width:${size === 16 ? 16 : Math.round(size * 0.82)}px;height:auto"/>
      </div>
      <span style="font:12px system-ui;color:#666">${label}</span>
    </div>`;
  await page.setContent(`
    <body style="margin:0;padding:24px;background:#f4f4f5;font-family:system-ui">
      <h3 style="margin:0 0 16px">${basename(f)}</h3>
      <div style="display:flex;gap:24px;align-items:flex-end">
        ${cell(360, "#ffffff", "360 light")}
        ${cell(360, "#0b0b10", "360 dark")}
        <div style="display:flex;flex-direction:column;gap:14px">
          ${cell(64, "#ffffff", "64 light")}
          ${cell(64, "#0b0b10", "64 dark")}
          ${cell(16, "#ffffff", "16")}
          ${cell(16, "#0b0b10", "16 dark")}
        </div>
      </div>
    </body>`);
  const out = resolve(dirname(f), basename(f).replace(/\.svg$/, ".render.png"));
  await page.screenshot({ path: out });
  console.log(out);
}
await browser.close();
