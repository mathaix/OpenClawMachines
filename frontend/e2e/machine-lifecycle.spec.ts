import { test, expect } from "@playwright/test";

// Serial: tests share the machine created in the first test and cleaned up in the last.
test.describe.serial("Machine lifecycle", () => {
  const machineName = `pw-test-${Date.now()}`;
  let machineDetailUrl = "";

  test("create a new machine", async ({ page }) => {
    await page.goto("/dashboard");

    // Click "New Machine" link
    await page.getByRole("link", { name: "New Machine" }).click();
    await expect(page).toHaveURL(/\/machines\/new/);

    // Fill machine name — "Standard" size is already selected by default
    await page.getByPlaceholder("Jarvis-01").fill(machineName);

    // Submit the form
    await page.getByRole("button", { name: "Create Machine" }).click();

    // Should redirect to the machine detail page
    await expect(page).toHaveURL(/\/dashboard\/machines\/[a-f0-9-]+/, {
      timeout: 15_000,
    });

    // Machine name should be visible as heading
    await expect(page.getByRole("heading", { name: machineName })).toBeVisible();

    // Machine should be in "stopped" state after creation
    await expect(page.getByText("stopped")).toBeVisible();
    await expect(page.getByRole("button", { name: "Start" })).toBeVisible();

    machineDetailUrl = page.url();
  });

  test("start machine and provisioning completes", async ({ page }) => {
    test.setTimeout(180_000);

    await page.goto(machineDetailUrl);

    // Machine should be stopped with Start button
    await expect(page.getByRole("button", { name: "Start" })).toBeVisible({
      timeout: 10_000,
    });

    // Click Start
    await page.getByRole("button", { name: "Start" }).click();

    // Should transition to provisioning — wait for provisioning progress to appear
    await expect(page.getByText("Provisioning", { exact: true })).toBeVisible({ timeout: 30_000 });

    // Wait for the final provisioning step to complete.
    // The "Open Workspace" link appears when status becomes "running".
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 150_000 });
  });

  test("workspace loads with terminal and log panels", async ({ page }) => {
    test.setTimeout(30_000);

    await page.goto(machineDetailUrl);

    // Wait for machine to be running and workspace link visible
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 15_000 });

    // Open the workspace
    await page.getByRole("link", { name: "Open Workspace" }).click();
    await expect(page).toHaveURL(/\/workspace\//);

    // Verify header shows machine name and running status
    await expect(page.getByText(machineName)).toBeVisible();
    await expect(page.getByText("running")).toBeVisible();

    // Verify the three panel headers
    await expect(page.getByText("OpenClaw Output")).toBeVisible();
    await expect(page.getByText("Shell")).toBeVisible();
    await expect(page.getByText("Browser View", { exact: true })).toBeVisible();

    // Verify terminals load (both xterm instances: log console + shell)
    await expect(page.locator(".xterm").first()).toBeVisible({ timeout: 10_000 });
    await expect(page.locator(".xterm")).toHaveCount(2);

    // Navigate back to machine detail
    await page.getByRole("link", { name: /Back/ }).click();
    await expect(page).toHaveURL(/\/dashboard\/machines\//);
  });

  test("machine detail tabs work while running", async ({ page }) => {
    await page.goto(machineDetailUrl);

    // Wait for running state
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 15_000 });

    // Overview tab shows "running" status
    await expect(page.getByText("running")).toBeVisible();

    // Logs tab — should load log console, not show "Start the machine"
    await page.getByRole("button", { name: "Logs" }).click();
    await expect(page.getByText("Start the machine to view logs.")).not.toBeVisible();

    // Output tab — should load file browser, not show "Start the machine"
    await page.getByRole("button", { name: "Output" }).click();
    await expect(page.getByText("Start the machine to browse output files.")).not.toBeVisible();

    // Back to Overview
    await page.getByRole("button", { name: "Overview" }).click();
  });

  test("openclaw agent is running inside the MicroVM", async ({ page }) => {
    test.setTimeout(60_000);

    await page.goto(machineDetailUrl);

    // Wait for running state
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).toBeVisible({ timeout: 15_000 });

    // Open workspace
    await page.getByRole("link", { name: "Open Workspace" }).click();
    await expect(page).toHaveURL(/\/workspace\//);

    // 1. Verify log output shows VM is ready
    const logPanel = page.locator(".xterm").first();
    await expect(logPanel).toBeVisible({ timeout: 10_000 });
    await expect(async () => {
      const text = await logPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("VM is ready");
    }).toPass({ timeout: 15_000 });

    // 2. Verify the shell has a connected prompt (openclaw@openclaw)
    const shellPanel = page.locator(".xterm").nth(1);
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("openclaw@openclaw");
    }).toPass({ timeout: 15_000 });

    // 3. Verify clawdbot gateway is running by checking its port via shell
    //    Type a command into the shell terminal and check the output
    await shellPanel.click();
    await page.keyboard.type("curl -sf http://localhost:3000/health && echo OK\n");

    // Wait for the output to contain "OK" (clawdbot gateway responded)
    await expect(async () => {
      const text = await shellPanel.locator(".xterm-rows").innerText();
      expect(text).toContain("OK");
    }).toPass({ timeout: 30_000 });

    // Navigate back
    await page.getByRole("link", { name: /Back/ }).click();
    await expect(page).toHaveURL(/\/dashboard\/machines\//);
  });

  test("stop the machine", async ({ page }) => {
    test.setTimeout(60_000);

    await page.goto(machineDetailUrl);

    // Wait for running state
    await expect(page.getByRole("button", { name: "Stop" })).toBeVisible({
      timeout: 15_000,
    });

    // Click Stop
    await page.getByRole("button", { name: "Stop" }).click();

    // Wait for status to transition to "stopped" — page polls every 5s
    await expect(page.getByText("stopped")).toBeVisible({ timeout: 30_000 });

    // Start and Delete buttons should appear
    await expect(page.getByRole("button", { name: "Start" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Delete" })).toBeVisible();

    // "Open Workspace" should no longer be visible
    await expect(
      page.getByRole("link", { name: "Open Workspace" })
    ).not.toBeVisible();
  });

  test("delete the machine", async ({ page }) => {
    await page.goto(machineDetailUrl);

    // Wait for stopped state with Delete button
    await expect(page.getByRole("button", { name: "Delete" })).toBeVisible({
      timeout: 15_000,
    });

    // Click Delete — confirmation dialog appears
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = page.locator(".fixed.inset-0").locator(".bg-white");
    await expect(dialog.getByText("Delete Machine")).toBeVisible();
    await expect(dialog.getByText(machineName)).toBeVisible();
    await dialog.getByRole("button", { name: "Delete" }).click();

    // Should redirect to dashboard
    await expect(page).toHaveURL(/\/dashboard$/, { timeout: 10_000 });

    // Machine should no longer appear in the list
    await expect(page.getByText(machineName)).not.toBeVisible();
  });
});
