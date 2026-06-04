import { test, expect } from "@playwright/test";

// Login tests run without saved auth state
test.use({ storageState: { cookies: [], origins: [] } });

test.describe("Login page", () => {
  test("renders login form with email and password fields", async ({
    page,
  }) => {
    await page.goto("/login");

    await expect(page.getByText("OpenClaw Machines")).toBeVisible();
    await expect(page.getByLabel("Email")).toBeVisible();
    await expect(page.getByLabel("Password")).toBeVisible();
    await expect(page.getByRole("button", { name: "Sign In" })).toBeVisible();
  });

  test("shows OAuth buttons", async ({ page }) => {
    await page.goto("/login");

    await expect(page.getByText("Continue with Google")).toBeVisible();
    await expect(page.getByText("Continue with GitHub")).toBeVisible();
  });

  test("toggles between sign in and sign up", async ({ page }) => {
    await page.goto("/login");

    // Default: sign in mode
    await expect(page.getByText("Sign in to manage your AI agents")).toBeVisible();
    await expect(page.getByLabel("Name")).not.toBeVisible();

    // Switch to sign up
    await page.getByRole("button", { name: "Sign up" }).click();
    await expect(page.getByText("Create an account")).toBeVisible();
    await expect(page.getByLabel("Name")).toBeVisible();

    // Switch back to sign in
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.getByText("Sign in to manage your AI agents")).toBeVisible();
  });

  test("shows error on invalid credentials", async ({ page }) => {
    await page.goto("/login");

    await page.getByLabel("Email").fill("bad@example.com");
    await page.getByLabel("Password").fill("wrongpassword");
    await page.getByRole("button", { name: "Sign In" }).click();

    // Wait for error message to appear
    await expect(page.locator(".bg-red-50")).toBeVisible({ timeout: 5_000 });
  });

  test("successful login redirects to dashboard", async ({ page }) => {
    const email =
      process.env.PLAYWRIGHT_TEST_EMAIL || "integtest@openclawmachines.com";
    const password = process.env.PLAYWRIGHT_TEST_PASSWORD || "TestPass123!";

    await page.goto("/login");

    await page.getByLabel("Email").fill(email);
    await page.getByLabel("Password").fill(password);
    await page.getByRole("button", { name: "Sign In" }).click();

    await expect(page).toHaveURL(/\/dashboard/, { timeout: 10_000 });
  });

  test("redirects to dashboard if already authenticated", async ({
    browser,
  }) => {
    const email =
      process.env.PLAYWRIGHT_TEST_EMAIL || "integtest@openclawmachines.com";
    const password = process.env.PLAYWRIGHT_TEST_PASSWORD || "TestPass123!";

    // First, log in to get cookies
    const context = await browser.newContext();
    const page = await context.newPage();
    await page.goto("/login");
    await page.getByLabel("Email").fill(email);
    await page.getByLabel("Password").fill(password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await expect(page).toHaveURL(/\/dashboard/, { timeout: 10_000 });

    // Now visit /login again — should redirect to dashboard
    await page.goto("/login");
    await expect(page).toHaveURL(/\/dashboard/, { timeout: 5_000 });

    await context.close();
  });
});
