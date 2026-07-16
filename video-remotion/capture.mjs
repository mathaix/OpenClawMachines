import { chromium } from "playwright";
import fs from "node:fs";

const OUT = "captures";
fs.mkdirSync(OUT, { recursive: true });
const BASE = "http://localhost:5173";
const WS = "d63e1ce7-22c5-4701-bb61-bbac3aa9834a"; // Default workspace id

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: 1920, height: 1080 },
  deviceScaleFactor: 2,
});
const page = await ctx.newPage();
const shot = async (name) => {
  await page.screenshot({ path: `${OUT}/${name}.png` });
  console.log("captured", name);
};
const settle = (ms = 900) => page.waitForTimeout(ms);

// 1. Dashboard
await page.goto(`${BASE}/dashboard`, { waitUntil: "networkidle" });
await settle();
await shot("01-dashboard");

// 2. New Machine modal, filled
await page.getByRole("button", { name: "New Machine" }).click();
await settle(500);
const nameBox = page.locator('input[type="text"]').first();
await nameBox.click();
await nameBox.fill("demo-agent");
await settle(400);
await shot("02-new-machine");

// 3. Create it -> dashboard updates
await page.getByRole("button", { name: "Create" }).click();
await settle(1500);
await page.goto(`${BASE}/dashboard`, { waitUntil: "networkidle" });
await settle();
await shot("03-created");

// 4. Workspaces list
await page.goto(`${BASE}/workspaces`, { waitUntil: "networkidle" });
await settle();
await shot("04-workspaces");

// 5. Workspace detail
await page.goto(`${BASE}/workspaces/${WS}`, { waitUntil: "networkidle" });
await settle();
await shot("05-workspace");

// 6. Integrations catalog (connect MCP tools)
await page.goto(`${BASE}/workspaces/${WS}/integrations`, { waitUntil: "networkidle" });
await settle(1200);
await shot("06-integrations");

// 7. Hover the GitHub Add to emphasize "connect"
try {
  const gh = page.getByText("Official GitHub MCP server", { exact: false }).first();
  await gh.scrollIntoViewIfNeeded();
  await settle(300);
  await shot("07-integrations-github");
} catch (e) {
  console.log("github shot skipped:", e.message);
}

await browser.close();
console.log("done");
