import { test, expect } from "@playwright/test";

test.describe("Settings", () => {
  test("loads settings page with tabs", async ({ page }) => {
    await page.goto("/dashboard/settings");

    await expect(page).toHaveURL(/\/settings/);

    // Should show tab navigation
    await expect(page.getByRole("button", { name: /profile/i })).toBeVisible();
    await expect(
      page.getByRole("button", { name: /api keys/i })
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: /usage/i })
    ).toBeVisible();
  });

  test("can switch to API Keys tab", async ({ page }) => {
    await page.goto("/dashboard/settings");

    await page.getByRole("button", { name: /api keys/i }).click();

    // API Keys tab should show provider names
    await expect(page.getByText(/anthropic/i)).toBeVisible({ timeout: 5_000 });
  });

  test("can switch to Usage & Billing tab", async ({ page }) => {
    await page.goto("/dashboard/settings");

    await page.getByRole("button", { name: /usage/i }).click();

    // Usage tab should show spend information
    await expect(page.getByText("Current Month Spend")).toBeVisible({ timeout: 5_000 });
  });
});
