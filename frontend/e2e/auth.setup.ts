import { test as setup, expect } from "@playwright/test";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const authFile = path.join(__dirname, ".auth/state.json");

setup("authenticate", async ({ page }) => {
  const email =
    process.env.PLAYWRIGHT_TEST_EMAIL || "integtest@openclawmachines.com";
  const password = process.env.PLAYWRIGHT_TEST_PASSWORD || "TestPass123!";

  await page.goto("/login");

  // Wait for login form to be ready
  await expect(page.getByLabel("Email")).toBeVisible({ timeout: 10_000 });

  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign In" }).click();

  // Wait for redirect to dashboard after successful login
  await expect(page).toHaveURL(/\/dashboard/, { timeout: 15_000 });

  // Save signed-in state (includes httpOnly cookies)
  await page.context().storageState({ path: authFile });
});
