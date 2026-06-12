// Capture deterministic animation frames of mascot3d.html into a contact sheet.
// Usage: node docs/branding/mascot3d/render-frames.mjs <out.png> [html]
// Frames: spin-up, mid-spin, decelerating, overshoot, settled — plus a big settled hero.
import { resolve, dirname } from "path";
import { pathToFileURL } from "url";

const root = resolve(dirname(new URL(import.meta.url).pathname), "../../..");
const { chromium } = await import(
  pathToFileURL(resolve(root, "frontend/node_modules/playwright/index.mjs")).href
);

const out = process.argv[2] || resolve(dirname(new URL(import.meta.url).pathname), "renders/latest.png");
const html = process.argv[3] || resolve(dirname(new URL(import.meta.url).pathname), "mascot3d.html");

const TIMES = [0.05, 0.4, 0.58, 0.85, 1.5, 99]; // drop, fall, impact-squash, recover, overshoot, settled
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
await page.goto(pathToFileURL(html).href);

const shots = [];
for (const t of TIMES) {
  await page.evaluate((tt) => window.renderAt(tt), t);
  shots.push(await page.locator("canvas").screenshot());
}

// compose contact sheet
const cells = TIMES.map((t, i) => {
  const b64 = shots[i].toString("base64");
  const label = t === 99 ? "settled" : `t=${t}s`;
  return `<div style="display:flex;flex-direction:column;align-items:center;gap:4px">
    <img src="data:image/png;base64,${b64}" style="width:${i === TIMES.length - 1 ? 300 : 190}px;border:1px solid #333;border-radius:6px"/>
    <span style="font:11px system-ui;color:#888">${label}</span></div>`;
}).join("");
await page.setContent(`<body style="margin:0;padding:14px;background:#16161d">
  <div style="display:flex;gap:10px;align-items:flex-end">${cells}</div></body>`);
await page.locator("body").screenshot({ path: out });
console.log(out);
await browser.close();
