import { test, expect } from "@playwright/test";

/**
 * Credential CRUD tests for all 3 LLM providers via the Settings UI.
 *
 * Requires env vars:
 *   TEST_ANTHROPIC_API_KEY, TEST_OPENAI_API_KEY, TEST_OPENROUTER_API_KEY
 *
 * Run:
 *   cd frontend && npx playwright test credentials-providers.spec.ts
 */

const providers = [
  {
    name: "Anthropic Claude",
    envVar: "TEST_ANTHROPIC_API_KEY",
    placeholder: "sk-ant-...",
  },
  {
    name: "OpenAI",
    envVar: "TEST_OPENAI_API_KEY",
    placeholder: "sk-...",
  },
  {
    name: "OpenRouter",
    envVar: "TEST_OPENROUTER_API_KEY",
    placeholder: "sk-or-v1-...",
  },
];

test.describe.serial("Credential management via Settings UI", () => {
  for (const provider of providers) {
    test(`add ${provider.name} credential`, async ({ page }) => {
      const apiKey = process.env[provider.envVar];
      test.skip(!apiKey, `${provider.envVar} not set — skipping`);

      await page.goto("/dashboard/settings");

      // Click the Integrations tab (default tab, but be explicit)
      await page.getByRole("button", { name: /integrations/i }).click();

      // Wait for providers to load
      await expect(page.getByText(provider.name)).toBeVisible({ timeout: 10_000 });

      // Find the provider card
      const card = page
        .locator(".rounded-lg.border")
        .filter({ hasText: provider.name });

      // Click "Add Key" or "Replace" depending on current state
      const replaceBtn = card.getByRole("button", { name: "Replace" });
      const addKeyBtn = card.getByRole("button", { name: "Add Key" });
      if (await replaceBtn.isVisible({ timeout: 1_000 }).catch(() => false)) {
        await replaceBtn.click();
      } else {
        await addKeyBtn.click();
      }

      // Fill the inline form
      await card.locator("input[type='password']").fill(apiKey!);
      await card.getByPlaceholder("e.g. Production key").fill("Playwright Test");

      // Save — the button validates the key against the provider
      await card.getByRole("button", { name: "Validate & Save" }).click();

      // Wait for save to complete — "Configured" badge appears
      await expect(card.getByText("Configured")).toBeVisible({ timeout: 30_000 });

      // The form should have closed (no more password input)
      await expect(card.locator("input[type='password']")).not.toBeVisible();
    });
  }

  test("all three providers show as configured", async ({ page }) => {
    test.skip(
      !process.env.TEST_ANTHROPIC_API_KEY ||
        !process.env.TEST_OPENAI_API_KEY ||
        !process.env.TEST_OPENROUTER_API_KEY,
      "Not all API keys set — skipping verification"
    );

    await page.goto("/dashboard/settings");
    await page.getByRole("button", { name: /integrations/i }).click();

    // All 3 should show "Configured" badge
    for (const provider of providers) {
      const card = page
        .locator(".rounded-lg.border")
        .filter({ hasText: provider.name });
      await expect(card.getByText("Configured")).toBeVisible({ timeout: 10_000 });
    }
  });
});
