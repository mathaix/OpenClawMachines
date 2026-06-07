import { test, expect } from "@playwright/test";

// Spot E2E smoke (CI: ci/spot-e2e.sh, project chromium-dev, AUTH_MODE=dev).
//
// Validates the provisioning path against a real spot host: create a machine,
// start it (Firecracker microVM boots and the machine reaches "running"), then
// stop + delete. It intentionally does NOT open the workspace / assert the
// in-VM OpenClaw gateway or terminals — those need the rootfs runtime staging
// and a deployed data-plane Worker, which are tracked separately. Run
// machine-lifecycle.spec.ts for the full workspace assertions once those land.
test.describe.serial("Spot host provisioning smoke", () => {
  const name = `ci-${Date.now()}`;
  let machineUrl = "";

  test("create a machine (placed on the spot host)", async ({ page }) => {
    await page.goto("/dashboard");
    await page.getByRole("link", { name: "New Machine" }).click();
    await expect(page).toHaveURL(/\/machines\/new/);
    await page.getByPlaceholder("Jarvis-01").fill(name);
    // Default size (Standard) and the auto-selected region (the ready host) are fine.
    await page.getByRole("button", { name: "Create Machine" }).click();
    await expect(page).toHaveURL(/\/dashboard\/machines\/[a-f0-9-]+/, { timeout: 15_000 });
    machineUrl = page.url();
    await expect(page.getByText("stopped")).toBeVisible();
  });

  test("start boots a Firecracker microVM (reaches running)", async ({ page }, testInfo) => {
    test.setTimeout(300_000);
    await page.goto(machineUrl);
    await page.getByRole("button", { name: "Start" }).click();
    // Placement + boot kicked off.
    await expect(page.getByText("Provisioning", { exact: true })).toBeVisible({ timeout: 30_000 });
    // The VM boots on the host and the machine flips to "running" — the
    // "Open Workspace" link is rendered only at status === running.
    await expect(page.getByRole("link", { name: "Open Workspace" })).toBeVisible({ timeout: 240_000 });
    // Labeled validation evidence in the artifact/report.
    await testInfo.attach("firecracker-running", {
      body: await page.screenshot({ fullPage: true }),
      contentType: "image/png",
    });
  });

  test("stop and delete", async ({ page }) => {
    test.setTimeout(120_000);
    await page.goto(machineUrl);
    await page.getByRole("button", { name: "Stop" }).click();
    await expect(page.getByText("stopped")).toBeVisible({ timeout: 60_000 });
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = page.locator(".fixed.inset-0").locator(".bg-white");
    await expect(dialog.getByText("Delete Machine")).toBeVisible();
    await dialog.getByRole("button", { name: "Delete" }).click();
    await expect(page).toHaveURL(/\/dashboard$/, { timeout: 10_000 });
  });
});
