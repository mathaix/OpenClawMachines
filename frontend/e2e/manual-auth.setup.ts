import { test as setup, expect } from "@playwright/test";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const authFile = path.join(__dirname, ".auth/state.json");

/**
 * Manual login helper for production testing.
 *
 * Run headed so you can log in manually (Google OAuth, etc.):
 *   cd frontend && npx playwright test manual-auth.setup.ts --headed --project=manual-auth
 *
 * The script opens the login page, pauses for you to authenticate,
 * then saves the auth state to e2e/.auth/state.json for reuse by other tests.
 */
setup("manual login", async ({ page }) => {
  setup.setTimeout(300_000); // 5 minutes to complete manual login

  await page.goto("/login");

  // Pause — complete login in the browser (Google OAuth, email/password, etc.)
  // When done and you see the dashboard, click the "Resume" button in Playwright Inspector.
  await page.pause();

  // After resume, verify we landed on the dashboard
  await expect(page).toHaveURL(/\/dashboard/, { timeout: 15_000 });

  // Save signed-in state (includes httpOnly cookies)
  await page.context().storageState({ path: authFile });
});
