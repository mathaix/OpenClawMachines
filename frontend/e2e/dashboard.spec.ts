import { test, expect } from "@playwright/test";

test.describe("Dashboard", () => {
  test("loads and shows page content", async ({ page }) => {
    await page.goto("/dashboard");

    // Should not redirect to login (auth state is loaded)
    await expect(page).toHaveURL(/\/dashboard/);

    // Should show the main layout with navigation
    await expect(page.getByText("OpenClaw Machines")).toBeVisible();
  });

  test("shows machine list or empty state", async ({ page }) => {
    await page.goto("/dashboard");

    // Either machines are listed or an empty state / "New Machine" button shows
    const content = page.locator("main");
    await expect(content).toBeVisible();
  });

  test("navigation links work", async ({ page }) => {
    await page.goto("/dashboard");

    // Click Settings link
    await page.getByRole("link", { name: /settings/i }).click();
    await expect(page).toHaveURL(/\/settings/);

    // Navigate back to dashboard
    await page.getByRole("link", { name: /dashboard/i }).click();
    await expect(page).toHaveURL(/\/dashboard/);
  });
});
