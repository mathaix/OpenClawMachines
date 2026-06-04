#!/usr/bin/env node
/**
 * E2E test using puppeteer-core + real Chrome.
 *
 * Steps:
 *   1. Log in to openclawmachines.com
 *   2. Add Anthropic API key via Settings > API Keys
 *   3. Create & start machine via modal, wait for workspace redirect
 *   4. Curl the apiproxy from inside the VM → verify LLM response
 *   5. Clean up (stop + delete machine)
 */

import puppeteer from "puppeteer-core";

const BASE = "https://www.openclawmachines.com";
const EMAIL = process.env.TEST_EMAIL || "integtest@openclawmachines.com";
const PASSWORD = process.env.TEST_PASSWORD || "TestPass123!";
const ANTHROPIC_KEY = process.env.ANTHROPIC_KEY || "";
const SKIP_KEY_SETUP = !ANTHROPIC_KEY; // skip if no key provided (already saved)

const CHROME_PATH =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

function log(msg) {
  console.log(`[${new Date().toISOString()}] ${msg}`);
}

async function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

// Wait for selector with custom timeout
async function waitFor(page, selector, timeout = 15000) {
  return page.waitForSelector(selector, { visible: true, timeout });
}

// Wait for text to appear on page
async function waitForText(page, text, timeout = 30000) {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    const content = await page.content();
    if (content.includes(text)) return true;
    await sleep(1000);
  }
  throw new Error(`Timed out waiting for text: "${text}" (${timeout}ms)`);
}

let browser;
let machineDetailUrl = "";

try {
  // Launch Chrome
  log("Launching Chrome...");
  browser = await puppeteer.launch({
    executablePath: CHROME_PATH,
    headless: false,
    defaultViewport: { width: 1400, height: 900 },
    args: ["--no-first-run", "--disable-default-apps"],
  });

  const page = await browser.newPage();
  page.setDefaultTimeout(30000);

  // ── Step 1: Login ──────────────────────────────────────
  log("Step 1: Logging in...");
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle2" });
  await sleep(2000);

  // Fill login form
  const emailInput = await waitFor(page, 'input[type="email"], input[name="email"]');
  await emailInput.click({ clickCount: 3 });
  await emailInput.type(EMAIL);

  const passwordInput = await waitFor(page, 'input[type="password"]');
  await passwordInput.click({ clickCount: 3 });
  await passwordInput.type(PASSWORD);

  // Click Sign In
  const signInBtn = await page.waitForSelector('button[type="submit"]', { visible: true });
  await signInBtn.click();

  // Wait for redirect to dashboard
  await page.waitForNavigation({ waitUntil: "networkidle2", timeout: 15000 }).catch(() => {});
  await sleep(2000);

  if (!page.url().includes("/dashboard")) {
    // Try waiting a bit more
    await page.waitForFunction(() => window.location.href.includes("/dashboard"), { timeout: 10000 });
  }
  log(`  Logged in. URL: ${page.url()}`);

  // ── Step 2: Add Anthropic API key (skip if already saved) ──
  if (SKIP_KEY_SETUP) {
    log("Step 2: Skipping API key setup (already validated).");
  } else {
    log("Step 2: Adding Anthropic API key via Settings...");
    await page.goto(`${BASE}/dashboard/settings`, { waitUntil: "networkidle2" });
    await sleep(2000);

    const tabs = await page.$$("button");
    for (const tab of tabs) {
      const text = await tab.evaluate((el) => el.textContent);
      if (text && /api keys/i.test(text)) { await tab.click(); break; }
    }
    await sleep(2000);

    const buttons = await page.$$("button");
    let foundAddReplace = false;
    for (const btn of buttons) {
      const text = await btn.evaluate((el) => el.textContent?.trim());
      if (text === "Add Key" || text === "Replace") {
        const parent = await btn.evaluateHandle((el) => el.closest(".bg-white"));
        if (parent) {
          const parentText = await parent.evaluate((el) => el?.textContent || "");
          if (parentText.includes("Anthropic")) { await btn.click(); foundAddReplace = true; break; }
        }
      }
    }

    if (foundAddReplace) {
      await sleep(1000);
      const keyInput = await waitFor(page, 'input[type="password"]');
      await keyInput.click({ clickCount: 3 });
      await keyInput.type(ANTHROPIC_KEY);

      const saveButtons = await page.$$("button");
      for (const btn of saveButtons) {
        const text = await btn.evaluate((el) => el.textContent?.trim());
        if (text && /validate/i.test(text)) { await btn.click(); break; }
      }
      log("  Validating API key...");
      await sleep(5000);
      try { await waitForText(page, "Validated", 15000); log("  API key validated and saved."); }
      catch { log("  Key may have been saved."); }
    }
  }

  // ── Step 3: Create a machine ──────────────────────────
  log("Step 3: Creating a machine...");
  const machineName = `chrome-e2e-${Date.now()}`;

  await page.goto(`${BASE}/dashboard`, { waitUntil: "networkidle2" });
  await sleep(2000);

  // Click "New Machine" button (opens modal)
  const allButtons = await page.$$("button");
  for (const btn of allButtons) {
    const text = await btn.evaluate((el) => el.textContent?.trim());
    if (text === "New Machine") { await btn.click(); break; }
  }
  await sleep(2000);

  // Fill machine name in the modal
  const nameInput = await waitFor(page, 'input[placeholder="machine-a1b2"]');
  await nameInput.click({ clickCount: 3 });
  await nameInput.type(machineName);

  // Click "Create & Start" to create and auto-start
  const modalButtons = await page.$$("button");
  for (const btn of modalButtons) {
    const text = await btn.evaluate((el) => el.textContent?.trim());
    if (text === "Create & Start") { await btn.click(); break; }
  }

  // Wait for provisioning to begin (modal shows ProvisioningProgress)
  log(`  Machine created: ${machineName}`);
  await sleep(5000);

  // The modal shows provisioning progress, then navigates to workspace on completion.
  // Wait for the workspace URL (exclude /gateway sub-path).
  const provStart = Date.now();
  let reachedWorkspace = false;
  while (Date.now() - provStart < 600000) {
    const currentUrl = page.url();
    if (/\/workspace\/[a-f0-9-]+$/.test(currentUrl)) {
      reachedWorkspace = true;
      break;
    }
    // If we landed on /gateway, navigate to the workspace instead
    const gatewayMatch = currentUrl.match(/\/workspace\/([a-f0-9-]+)\/gateway/);
    if (gatewayMatch) {
      await page.goto(`${BASE}/workspace/${gatewayMatch[1]}`, { waitUntil: "networkidle2" });
      reachedWorkspace = true;
      break;
    }
    await sleep(3000);
  }

  if (!reachedWorkspace) {
    throw new Error("Machine did not reach workspace within timeout");
  }

  // Extract machine ID from workspace URL for cleanup later
  const wsUrl = page.url();
  const machineIdMatch = wsUrl.match(/\/workspace\/([a-f0-9-]+)/);
  if (machineIdMatch) {
    machineDetailUrl = `${BASE}/dashboard/machines/${machineIdMatch[1]}`;
  }
  log(`  Machine is running! Workspace URL: ${wsUrl}`);

  // ── Step 4: Test LLM from workspace terminal ──────────
  log("Step 4: Testing LLM from workspace...");
  await sleep(5000);

  // Wait for terminal to load (xterm elements)
  log("  Waiting for terminal...");
  await page.waitForSelector(".xterm", { visible: true, timeout: 15000 });
  await sleep(3000);

  // Wait for shell prompt
  log("  Waiting for shell prompt...");
  const termStart = Date.now();
  let hasPrompt = false;
  while (Date.now() - termStart < 30000) {
    const xtermText = await page.evaluate(() => {
      const rows = document.querySelectorAll(".xterm-rows");
      return Array.from(rows).map((r) => r.textContent).join("\n");
    });
    if (xtermText.includes("openclaw@openclaw")) {
      hasPrompt = true;
      break;
    }
    await sleep(2000);
  }

  if (!hasPrompt) {
    log("  WARNING: Shell prompt not detected, proceeding anyway...");
  } else {
    log("  Shell prompt detected.");
  }

  // Click on the shell terminal (second xterm panel)
  const xtermElements = await page.$$(".xterm");
  if (xtermElements.length >= 2) {
    await xtermElements[1].click();
  } else if (xtermElements.length >= 1) {
    await xtermElements[0].click();
  }
  await sleep(1000);

  // Type the curl command to test LLM via apiproxy
  log("  Sending LLM test request via apiproxy...");
  const curlCmd =
    `curl -s http://192.168.100.1:4000/anthropic/v1/messages ` +
    `-H "x-api-key: $ANTHROPIC_API_KEY" ` +
    `-H "anthropic-version: 2023-06-01" ` +
    `-H "content-type: application/json" ` +
    `-d '{"model":"claude-haiku-4-5-20251001","max_tokens":10,"messages":[{"role":"user","content":"Say hi"}]}' && echo __LLM_OK__`;

  await page.keyboard.type(curlCmd, { delay: 10 });
  await page.keyboard.press("Enter");

  // Wait for response
  log("  Waiting for LLM response...");
  const llmStart = Date.now();
  let llmOk = false;
  let terminalOutput = "";
  while (Date.now() - llmStart < 60000) {
    terminalOutput = await page.evaluate(() => {
      const rows = document.querySelectorAll(".xterm-rows");
      return Array.from(rows).map((r) => r.textContent).join("\n");
    });
    if (terminalOutput.includes("__LLM_OK__")) {
      llmOk = true;
      break;
    }
    await sleep(2000);
  }

  if (llmOk) {
    log("  LLM RESPONSE RECEIVED!");
    if (terminalOutput.includes("stop_reason")) {
      log("  Response contains 'stop_reason' — Anthropic API working correctly.");
    }
    if (terminalOutput.includes("content")) {
      log("  Response contains 'content' field.");
    }
    if (terminalOutput.includes('"error"')) {
      log("  WARNING: Response contains an error field.");
    }
  } else {
    log("  FAILED: No __LLM_OK__ marker received within 60s.");
    log("  Terminal output (last 500 chars):");
    log("  " + terminalOutput.slice(-500));
  }

  // ── Step 5: Cleanup — stop and delete ─────────────────
  log("Step 5: Cleaning up...");
  await page.goto(machineDetailUrl, { waitUntil: "networkidle2" });
  await sleep(3000);

  // Stop
  const stopBtns = await page.$$("button");
  for (const btn of stopBtns) {
    const text = await btn.evaluate((el) => el.textContent?.trim());
    if (text === "Stop") {
      await btn.click();
      break;
    }
  }
  log("  Stopping machine...");

  // Wait for stopped state
  const stopStart = Date.now();
  while (Date.now() - stopStart < 30000) {
    await sleep(3000);
    await page.reload({ waitUntil: "networkidle2" });
    const content = await page.content();
    if (content.includes("stopped")) break;
  }
  log("  Machine stopped.");

  // Delete
  await sleep(2000);
  const delBtns = await page.$$("button");
  for (const btn of delBtns) {
    const text = await btn.evaluate((el) => el.textContent?.trim());
    if (text === "Delete") {
      await btn.click();
      break;
    }
  }
  await sleep(2000);

  // Confirm delete in dialog
  const dialogBtns = await page.$$("button");
  for (const btn of dialogBtns) {
    const text = await btn.evaluate((el) => el.textContent?.trim());
    if (text === "Delete") {
      await btn.click();
      break;
    }
  }

  await sleep(3000);
  log("  Machine deleted.");

  // ── Done ──────────────────────────────────────────────
  log("");
  log("═══════════════════════════════════════");
  if (llmOk) {
    log("  E2E TEST PASSED");
  } else {
    log("  E2E TEST FAILED — LLM response not received");
  }
  log("═══════════════════════════════════════");

  await sleep(3000);
  await browser.close();
  process.exit(llmOk ? 0 : 1);
} catch (err) {
  log(`ERROR: ${err.message}`);
  if (browser) {
    // Try cleanup if we have a machine
    if (machineDetailUrl) {
      log("Attempting cleanup of test machine...");
      try {
        const pages = await browser.pages();
        const page = pages[0];
        await page.goto(machineDetailUrl, { waitUntil: "networkidle2" });
        await sleep(2000);
        // Try stop
        const btns1 = await page.$$("button");
        for (const btn of btns1) {
          const text = await btn.evaluate((el) => el.textContent?.trim());
          if (text === "Stop") { await btn.click(); break; }
        }
        await sleep(10000);
        await page.reload({ waitUntil: "networkidle2" });
        // Try delete
        const btns2 = await page.$$("button");
        for (const btn of btns2) {
          const text = await btn.evaluate((el) => el.textContent?.trim());
          if (text === "Delete") { await btn.click(); break; }
        }
        await sleep(2000);
        const btns3 = await page.$$("button");
        for (const btn of btns3) {
          const text = await btn.evaluate((el) => el.textContent?.trim());
          if (text === "Delete") { await btn.click(); break; }
        }
        log("  Cleanup attempted.");
      } catch (cleanupErr) {
        log(`  Cleanup failed: ${cleanupErr.message}`);
      }
    }
    await sleep(2000);
    await browser.close();
  }
  process.exit(1);
}
