// Render a PNG mascot candidate IN CONTEXT (navbar/hero/card). PNG variant of ../render-context.mjs
import { readFileSync } from "fs";
import { resolve, dirname, basename } from "path";
import { pathToFileURL } from "url";
const root = resolve(dirname(new URL(import.meta.url).pathname), "../../..");
const { chromium } = await import(
  pathToFileURL(resolve(root, "frontend/node_modules/playwright/index.mjs")).href
);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1080, height: 860 } });
for (const f of process.argv.slice(2)) {
  const enc = `data:image/png;base64,${readFileSync(f).toString("base64")}`;
  await page.setContent(`
  <body style="margin:0;background:#e8e8ec;font-family:system-ui">
    <div style="padding:18px 24px 6px;font:600 15px system-ui">${basename(f)} — in context</div>

    <!-- dark app navbar, 28px logo -->
    <div style="background:#16161c;display:flex;align-items:center;gap:10px;padding:12px 24px;margin:8px 16px;border-radius:10px">
      <img src="${enc}" style="height:28px"/>
      <span style="color:#f97316;font-weight:700;font-size:16px">OpenClaw</span>
      <span style="color:#2dd4bf;font-weight:700;font-size:16px">Machines</span>
      <span style="color:#9ca3af;font-size:13px;margin-left:18px">Dashboard</span>
      <span style="color:#9ca3af;font-size:13px">Admin</span>
      <span style="color:#9ca3af;font-size:13px">Observability</span>
      <span style="margin-left:auto;color:#e5e7eb;background:#26262e;font-size:12px;padding:5px 12px;border-radius:999px">dev@localhost</span>
    </div>

    <!-- README hero on white, 96px logo -->
    <div style="background:#ffffff;margin:8px 16px;border-radius:10px;padding:28px 32px;display:flex;gap:24px;align-items:center">
      <img src="${enc}" style="height:96px"/>
      <div>
        <div style="font:800 26px system-ui;color:#111">OpenClaw Machines</div>
        <div style="font:15px system-ui;color:#555;margin-top:6px">Run as many isolated OpenClaw agents as you need, on hardware you own.</div>
        <div style="margin-top:10px;display:flex;gap:8px">
          <span style="background:#f3f4f6;border:1px solid #e5e7eb;font:12px system-ui;padding:4px 10px;border-radius:6px">Apache-2.0</span>
          <span style="background:#f3f4f6;border:1px solid #e5e7eb;font:12px system-ui;padding:4px 10px;border-radius:6px">CI passing</span>
        </div>
      </div>
    </div>

    <!-- dark dashboard card with small logo as machine avatar -->
    <div style="background:#0e0e13;margin:8px 16px;border-radius:10px;padding:22px 24px">
      <div style="color:#e5e7eb;font:700 15px system-ui;margin-bottom:14px">Machines</div>
      <div style="background:#17171e;border:1px solid #26262e;border-radius:10px;padding:14px 16px;display:flex;align-items:center;gap:12px;max-width:560px">
        <img src="${enc}" style="height:34px"/>
        <div>
          <div style="color:#f3f4f6;font:600 14px system-ui">research-agent</div>
          <div style="color:#8b8b94;font:12px system-ui">Created 2m ago · running</div>
        </div>
        <span style="margin-left:auto;color:#34d399;background:rgba(52,211,153,.12);font:12px system-ui;padding:4px 10px;border-radius:999px">Running</span>
      </div>
    </div>
  </body>`);
  const out = resolve(dirname(f), basename(f).replace(/\.png$/, ".context.png"));
  await page.screenshot({ path: out });
  console.log(out);
}
await browser.close();
