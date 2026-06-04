import { test, expect } from "@playwright/test";

/**
 * Full VM round-trip tests for all 3 LLM providers (Anthropic, OpenAI, OpenRouter).
 *
 * Creates one machine, sends LLM requests through the apiproxy for each provider,
 * then cleans up. Requires credentials to already be configured (run
 * credentials-providers.spec.ts first).
 *
 * Run:
 *   cd frontend && npx playwright test llm-providers-roundtrip.spec.ts
 */

test.describe.serial("LLM providers round-trip via apiproxy", () => {
  const machineName = `llm-providers-${Date.now()}`;
  let machineDetailUrl = "";

  test("create and start a machine", async ({ page }) => {
    test.setTimeout(240_000);

    // Create machine
    await page.goto("/dashboard");
    await page.getByRole("link", { name: "New Machine" }).click();
    await expect(page).toHaveURL(/\/machines\/new/);
    await page.getByPlaceholder("Jarvis-01").fill(machineName);
    await page.getByRole("button", { name: "Create Machine" }).click();

    // Wait for machine detail page
    await expect(page).toHaveURL(/\/dashboard\/machines\/[a-f0-9-]+/, {
      timeout: 15_000,
    });
    machineDetailUrl = page.url();

    // Start the machine
    await expect(page.getByRole("button", { name: "Start" })).toBeVisible({
      timeout: 10_000,
    });
    await page.getByRole("button", { name: "Start" }).click();

    // Wait for provisioning to complete
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 210_000 });
  });

  test("Anthropic LLM request through apiproxy", async ({ page }) => {
    test.setTimeout(120_000);

    await page.goto(machineDetailUrl);
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 15_000 });

    // Open workspace
    await page.getByRole("link", { name: "Open Workspace" }).click();
    await expect(page).toHaveURL(/\/workspace\//);

    // Wait for shell prompt
    const shellPanel = page.locator(".xterm").nth(1);
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("openclaw@openclaw");
    }).toPass({ timeout: 30_000 });

    // Send Anthropic API request through apiproxy
    await shellPanel.click();
    await page.keyboard.type(
      `curl -s http://192.168.100.1:4000/anthropic/v1/messages ` +
        `-H "x-api-key: $ANTHROPIC_API_KEY" ` +
        `-H "anthropic-version: 2023-06-01" ` +
        `-H "content-type: application/json" ` +
        `-d '{"model":"claude-haiku-4-5-20251001","max_tokens":10,"messages":[{"role":"user","content":"Say hi"}]}' && echo __ANTHROPIC_OK__\n`
    );

    // Wait for marker (curl exited 0)
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("__ANTHROPIC_OK__");
    }).toPass({ timeout: 60_000 });

    // Verify Anthropic response fields
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("stop_reason");
    }).toPass({ timeout: 5_000 });
  });

  test("OpenAI LLM request through apiproxy", async ({ page }) => {
    test.setTimeout(120_000);

    await page.goto(machineDetailUrl);
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 15_000 });

    // Open workspace
    await page.getByRole("link", { name: "Open Workspace" }).click();
    await expect(page).toHaveURL(/\/workspace\//);

    // Wait for shell prompt
    const shellPanel = page.locator(".xterm").nth(1);
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("openclaw@openclaw");
    }).toPass({ timeout: 30_000 });

    // Send OpenAI API request through apiproxy
    await shellPanel.click();
    await page.keyboard.type(
      `curl -s http://192.168.100.1:4000/openai/v1/chat/completions ` +
        `-H "Authorization: Bearer $OPENAI_API_KEY" ` +
        `-H "content-type: application/json" ` +
        `-d '{"model":"gpt-4o-mini","max_tokens":10,"messages":[{"role":"user","content":"Say hi"}]}' && echo __OPENAI_OK__\n`
    );

    // Wait for marker
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("__OPENAI_OK__");
    }).toPass({ timeout: 60_000 });

    // Verify OpenAI response fields
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("choices");
    }).toPass({ timeout: 5_000 });
  });

  test("OpenRouter LLM request through apiproxy", async ({ page }) => {
    test.setTimeout(120_000);

    await page.goto(machineDetailUrl);
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 15_000 });

    // Open workspace
    await page.getByRole("link", { name: "Open Workspace" }).click();
    await expect(page).toHaveURL(/\/workspace\//);

    // Wait for shell prompt
    const shellPanel = page.locator(".xterm").nth(1);
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("openclaw@openclaw");
    }).toPass({ timeout: 30_000 });

    // Send OpenRouter API request through apiproxy
    await shellPanel.click();
    await page.keyboard.type(
      `curl -s http://192.168.100.1:4000/openrouter/api/v1/chat/completions ` +
        `-H "Authorization: Bearer $OPENROUTER_API_KEY" ` +
        `-H "content-type: application/json" ` +
        `-d '{"model":"openai/gpt-4o-mini","max_tokens":10,"messages":[{"role":"user","content":"Say hi"}]}' && echo __OPENROUTER_OK__\n`
    );

    // Wait for marker
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("__OPENROUTER_OK__");
    }).toPass({ timeout: 60_000 });

    // Verify OpenRouter response fields (OpenAI-compatible format)
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("choices");
    }).toPass({ timeout: 5_000 });
  });

  test("cleanup: stop and delete machine", async ({ page }) => {
    test.setTimeout(60_000);

    await page.goto(machineDetailUrl);

    // Stop
    await expect(page.getByRole("button", { name: "Stop" })).toBeVisible({
      timeout: 15_000,
    });
    await page.getByRole("button", { name: "Stop" }).click();
    await expect(page.getByText("stopped")).toBeVisible({ timeout: 30_000 });

    // Delete
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = page.locator(".fixed.inset-0").locator(".bg-white");
    await expect(dialog.getByText("Delete Machine")).toBeVisible();
    await dialog.getByRole("button", { name: "Delete" }).click();
    await expect(page).toHaveURL(/\/dashboard$/, { timeout: 10_000 });
  });
});
